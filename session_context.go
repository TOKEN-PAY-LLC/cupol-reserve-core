package main

// contextPlaceholder — дефолт флага --url. Трактуется как «не задано», поэтому
// в вывод ключа не попадает как значимая строка; при этом он же остаётся
// запасным вариантом, чтобы узел без документа (direct, oneme) сохранил
// прежний ключ.
const contextPlaceholder = "http://#"

// pickSessionContext возвращает контекст вывода ключа шифрования.
//
// Порядок ровно такой же, как у клиента OpenFlux-Android и у CLI upstream —
// иначе ключи разъедутся, и каждый пакет будет молча отбрасываться как
// неаутентифицированный (снаружи это выглядит как таймауты, а не как ошибка):
//
//	явный      --session-context, если задан
//	--url      globalURL, если задан и не плейсхолдер
//	запасное   плейсхолдер "http://#"
//
// У нас один транспорт на процесс, поэтому промежуточного шага «URL самого
// приоритетного транспорта» нет: его URL и есть --url.
func pickSessionContext(explicit, globalURL string) string {
	if explicit != "" {
		return explicit
	}
	if globalURL != "" && globalURL != contextPlaceholder {
		return globalURL
	}
	return contextPlaceholder
}
