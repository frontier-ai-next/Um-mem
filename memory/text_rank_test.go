package memory

import "testing"

func TestRankTextPrefersTitleAndMatchesUnicode(t *testing.T) {
	documents := []string{"How to restart the local service", "A service note mentioning restart", "How to inspect logs", "Database backup steps"}
	titles := []string{"Restart local service", "Notes", "Inspect logs", "Backup"}
	ranked := RankText("restart service", documents, titles, 3)
	if len(ranked) != 1 || ranked[0].Index != 0 || ranked[0].Score != 1 ||
		len(ranked[0].Terms) != 2 || ranked[0].Terms[0] != "restart" || ranked[0].Terms[1] != "service" {
		t.Fatalf("title routing changed: %+v", ranked)
	}
	unicode := RankText("перезапустить сервис", []string{"Перезапуск сервиса после изменения конфигурации", "Другой процесс"},
		[]string{"Как перезапустить сервис", "Другое"}, 2)
	if len(unicode) != 1 || unicode[0].Index != 0 || unicode[0].Score != 1 ||
		len(unicode[0].Terms) != 2 || unicode[0].Terms[0] != "перезапустить" || unicode[0].Terms[1] != "сервис" {
		t.Fatalf("Unicode lexical matching changed: %+v", unicode)
	}
}
