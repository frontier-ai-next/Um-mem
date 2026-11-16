"use strict";

const crypto = require("crypto");
const fs = require("fs");
const https = require("https");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");
const { Transform } = require("stream");
const { pipeline } = require("stream/promises");
const { version: packageVersion, repository } = require("../package.json");

const programs = ["um-memory", "um-memory-mcp", "um-memory-service", "um-memory-hook"];
const platformNames = { win32: "windows", linux: "linux", darwin: "darwin" };
const archNames = { x64: "amd64", arm64: "arm64" };
const maxAssetBytes = 300 * 1024 * 1024;

function umMemHome() {
  return process.env.UM_MEM_HOME
    ? path.resolve(process.env.UM_MEM_HOME)
    : path.join(os.homedir(), ".um-mem");
}

function binRoot() { return path.join(umMemHome(), "bin"); }

function target() {
  const system = platformNames[process.platform];
  const architecture = archNames[process.arch];
  if (!system || !architecture) {
    throw new Error(`UM-MEM has no Go binary for ${process.platform}/${process.arch}`);
  }
  return { system, architecture, suffix: system === "windows" ? ".exe" : "" };
}

function assetName(program, selected = target()) {
  return `${program}-${selected.system}-${selected.architecture}${selected.suffix}`;
}

function installedDirectory() {
  let marker;
  try {
    marker = JSON.parse(fs.readFileSync(path.join(umMemHome(), "npm.json"), "utf8"));
  } catch {
    return "";
  }
  if (marker.runtime !== "go" || typeof marker.binDir !== "string") return "";
  const directory = path.resolve(marker.binDir);
  const relative = path.relative(binRoot(), directory);
  if (!relative || relative.startsWith("..") || path.isAbsolute(relative)) return "";
  const { suffix } = target();
  if (!programs.every((program) => fs.existsSync(path.join(directory, program + suffix)))) {
    return "";
  }
  return directory;
}

function programBin(program) {
  return path.join(installedDirectory() || binRoot(), program + target().suffix);
}

function engineBin() { return programBin("um-memory"); }
function mcpBin() { return programBin("um-memory-mcp"); }
function serviceBin() { return programBin("um-memory-service"); }
function hookBin() { return programBin("um-memory-hook"); }

function parseArgs(args) {
  const options = { from: "", version: process.env.UM_MEM_RELEASE_TAG || `v${packageVersion}`, force: false };
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === "--from") options.from = args[++index] || "";
    else if (argument === "--version") options.version = args[++index] || "";
    else if (argument === "--force") options.force = true;
    else throw new Error(`unknown install option: ${argument}`);
  }
  if (options.from && !fs.existsSync(options.from)) throw new Error("--from path does not exist");
  if (!/^v[0-9][A-Za-z0-9.+_-]*$/.test(options.version)) {
    throw new Error("Go release tag must begin with v and contain only version characters");
  }
  return options;
}

function sourceCheckout(directory) {
  return fs.existsSync(path.join(directory, "go.mod")) &&
    fs.existsSync(path.join(directory, "cmd", "um-memory", "main.go"));
}

function runGoBuild(source, staging) {
  const executable = process.env.UM_MEM_GO || "go";
  const { suffix } = target();
  for (const program of programs) {
    const result = spawnSync(executable,
      ["build", "-tags", "purego", "-trimpath", "-o", path.join(staging, program + suffix),
        `./cmd/${program}`],
      { cwd: source, stdio: "inherit", env: { ...process.env, CGO_ENABLED: "0" } });
    if (result.error) throw result.error;
    if (result.status !== 0) throw new Error(`Go build failed for ${program}`);
  }
}

function expectedHashes(manifest) {
  const hashes = new Map();
  for (const line of manifest.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const match = /^([a-fA-F0-9]{64})  ([A-Za-z0-9._-]+)$/.exec(line);
    if (!match || hashes.has(match[2])) throw new Error("Go release checksum manifest is invalid");
    hashes.set(match[2], match[1].toLowerCase());
  }
  for (const program of programs) {
    if (!hashes.has(assetName(program))) throw new Error(`Go release omits ${assetName(program)}`);
  }
  return hashes;
}

function sha256(file) {
  const hash = crypto.createHash("sha256");
  const descriptor = fs.openSync(file, "r");
  const chunk = Buffer.allocUnsafe(1024 * 1024);
  try {
    for (;;) {
      const read = fs.readSync(descriptor, chunk, 0, chunk.length, null);
      if (read === 0) break;
      hash.update(chunk.subarray(0, read));
    }
  } finally {
    fs.closeSync(descriptor);
  }
  return hash.digest("hex");
}

function installAssets(sourceDirectory, staging, manifest) {
  const hashes = expectedHashes(manifest);
  const { suffix } = target();
  for (const program of programs) {
    const name = assetName(program);
    const source = path.join(sourceDirectory, name);
    if (!fs.statSync(source).isFile() || sha256(source) !== hashes.get(name)) {
      throw new Error(`Go binary checksum mismatch: ${name}`);
    }
    const destination = path.join(staging, program + suffix);
    fs.copyFileSync(source, destination);
    fs.chmodSync(destination, 0o755);
  }
}

function releaseAssetURL(tag, name) {
  const repositoryURL = new URL(repository.url.startsWith("git+") ? repository.url.slice(4) : repository.url);
  const repositoryPath = repositoryURL.pathname.endsWith(".git")
    ? repositoryURL.pathname.slice(0, -4) : repositoryURL.pathname;
  if (repositoryURL.protocol !== "https:" || repositoryURL.hostname !== "github.com" ||
      repositoryPath.split("/").filter(Boolean).length !== 2) {
    throw new Error("Package repository must name a GitHub HTTPS repository");
  }
  return `${repositoryURL.origin}${repositoryPath}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(name)}`;
}

function trustedReleaseURL(value, redirects) {
  const parsed = new URL(value);
  const allowedHosts = redirects === 0 ? ["github.com"]
    : ["github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"];
  if (parsed.protocol !== "https:" || !allowedHosts.includes(parsed.hostname) ||
      parsed.username || parsed.password || parsed.port) {
    throw new Error("Go release URL must use trusted GitHub HTTPS");
  }
  return parsed;
}

function download(url, destination, redirects = 0) {
  if (redirects > 5) return Promise.reject(new Error("Go release redirect limit exceeded"));
  return new Promise((resolve, reject) => {
    const parsed = trustedReleaseURL(url, redirects);
    const request = https.get(parsed, (response) => {
      if ([301, 302, 303, 307, 308].includes(response.statusCode)) {
        const location = response.headers.location;
        response.resume();
        if (!location) return reject(new Error("Go release redirect has no location"));
        return resolve(download(new URL(location, url).toString(), destination, redirects + 1));
      }
      if (response.statusCode !== 200) {
        response.resume();
        return reject(new Error(`Go release download returned HTTP ${response.statusCode}`));
      }
      let received = 0;
      const limiter = new Transform({ transform(chunk, encoding, done) {
        received += chunk.length;
        if (received > maxAssetBytes) done(new Error("Go release asset exceeds size limit"));
        else done(null, chunk);
      } });
      pipeline(response, limiter, fs.createWriteStream(destination, { mode: 0o600 })).then(
        resolve, reject,
      );
    });
    request.setTimeout(30_000, () => request.destroy(new Error("Go release download timed out")));
    request.on("error", reject);
  });
}

async function downloadRelease(tag, staging) {
  const manifestPath = path.join(staging, "SHA256SUMS");
  await download(releaseAssetURL(tag, "SHA256SUMS"), manifestPath);
  const hashes = expectedHashes(fs.readFileSync(manifestPath, "utf8"));
  const { suffix } = target();
  for (const program of programs) {
    const name = assetName(program);
    const destination = path.join(staging, program + suffix);
    await download(releaseAssetURL(tag, name), destination);
    if (sha256(destination) !== hashes.get(name)) {
      throw new Error(`Go binary checksum mismatch: ${name}`);
    }
    fs.chmodSync(destination, 0o755);
  }
  fs.unlinkSync(manifestPath);
}

function removeStaging(staging) {
  const root = path.resolve(binRoot());
  const candidate = path.resolve(staging);
  const relative = path.relative(root, candidate);
  if (!relative.startsWith(".go-install-") || relative.includes(path.sep)) {
    throw new Error("refusing to remove a path outside the Go installation staging directory");
  }
  fs.rmSync(candidate, { recursive: true, force: true });
}

async function ensureEngine(rawArgs) {
  const options = parseArgs(rawArgs || []);
  const current = installedDirectory();
  const requestedSource = options.from ? path.resolve(options.from) : "";
  const installed = current
    ? JSON.parse(fs.readFileSync(path.join(umMemHome(), "npm.json"), "utf8"))
    : null;
  if (current && !options.force && !requestedSource && installed.version === options.version) {
    console.log(`UM-MEM Go is already installed at ${current}`);
    return 0;
  }
  fs.mkdirSync(binRoot(), { recursive: true, mode: 0o700 });
  const staging = fs.mkdtempSync(path.join(binRoot(), ".go-install-"));
  let source = requestedSource;
  if (!source && sourceCheckout(path.resolve(__dirname, "../.."))) {
    source = path.resolve(__dirname, "../..");
  }
  try {
    if (source && sourceCheckout(source)) {
      runGoBuild(source, staging);
    } else if (source) {
      const manifestPath = path.join(source, "SHA256SUMS");
      if (!fs.existsSync(manifestPath)) throw new Error("--from must name a Go checkout or verified release directory");
      installAssets(source, staging, fs.readFileSync(manifestPath, "utf8"));
    } else {
      await downloadRelease(options.version, staging);
    }
    const destination = path.join(binRoot(), `go-${Date.now()}-${crypto.randomBytes(6).toString("hex")}`);
    fs.renameSync(staging, destination);
    const marker = { runtime: "go", version: options.version, installedAt: new Date().toISOString(),
      source: source || "github-release", binDir: destination };
    const markerPath = path.join(umMemHome(), "npm.json");
    const temporary = markerPath + `.tmp-${process.pid}`;
    fs.writeFileSync(temporary, JSON.stringify(marker, null, 2) + "\n", { mode: 0o600 });
    fs.renameSync(temporary, markerPath);
    console.log(`UM-MEM Go installed at ${destination}`);
    console.log("Connect a host with: um-memory install-mcp --host codex|claude|hermes --repo-root ... --workspace ... --backend ...");
    return 0;
  } catch (error) {
    if (fs.existsSync(staging)) removeStaging(staging);
    throw error;
  }
}

module.exports = { ensureEngine, engineBin, mcpBin, serviceBin, hookBin, umMemHome,
  assetName, expectedHashes, installAssets, releaseAssetURL, trustedReleaseURL, sourceCheckout };
