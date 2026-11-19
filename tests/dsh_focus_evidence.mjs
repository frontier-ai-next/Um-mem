import assert from 'node:assert/strict'
import { adaptiveFocus } from '../integrations/dsh/lib/evidence.js'

const project = { primary_block_id: 'project' }
const confirmed = { block_membership: project, content: 'Night relay capacity limits confirmed by dispatcher.',
  evidence_refs: [{ ref_type: 'message', produced_by: 'human', producer_id: 'harness-user' }] }
const stale = { block_membership: project, content: 'Night relay capacity limits unknown. Investigate arrival window.' }
const independent = { block_membership: { primary_block_id: 'weather' }, content: 'Protected arrival window during rain.' }
assert.deepEqual(adaptiveFocus([confirmed, stale], 'arrival window'), [confirmed])
assert.deepEqual(adaptiveFocus([confirmed, stale, independent], 'arrival window'), [confirmed, independent])
console.log('Source-backed focus suppresses an overlapping interim note and retains distinct evidence.')
