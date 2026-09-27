package transport_test

import (
	"openflux/transport"
	"openflux/transport/script"
	"openflux/transport/yandex"
)

// Compile-time guarantees that every HTTP-based transport implements
// CookieExchanger. If any of these break, main.go's type assertion will
// silently skip control wiring and cookie exchange will be dead.
//
// mailru, boards and cupsonline are script-only now (transport/script/js/
// {mailru,boards,cupsonline}.js) - their CookieExchanger guarantee is the
// generic script.ScriptTransport one below, not a type-specific assertion.
var (
	_ transport.CookieExchanger = (*yandex.YandexDocsTransport)(nil)
	_ transport.CookieExchanger = (*yandex.YandexVolgaTransport)(nil)
	// Every script transport gets this for free (transport/script's shared
	// cookiejar.Jar) - see ScriptTransport.FetchCookies/ApplyCookies.
	_ transport.CookieExchanger = (*script.ScriptTransport)(nil)
)
