//go:build ios

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"

	"universal-bypass-tool/share"
)

// Мост к пакету share для приложения.
//
// Разбор и сборку openflux://-ссылки намеренно НЕ дублируем на Swift: формат
// это flate + base64url + JSON + набор правил валидации, и вторая реализация
// рано или поздно разъехалась бы с CLI. Приложение отдаёт сюда строку и
// получает JSON — одна реализация на все платформы, поведение гарантированно
// совпадает с узлом.

type shareResult struct {
	Error  string        `json:"error,omitempty"`
	Config *share.Config `json:"config,omitempty"`
	Link   string        `json:"link,omitempty"`
}

func shareJSON(r shareResult) *C.char {
	b, err := json.Marshal(r)
	if err != nil {
		return C.CString(`{"error":"marshal failed"}`)
	}
	return C.CString(string(b))
}

// OpenFluxShareDecode разбирает openflux://-ссылку и возвращает JSON вида
// {"config":{...}} либо {"error":"..."}. Результат C-аллоцирован; освобождать
// OpenFluxFreeString.
//
//export OpenFluxShareDecode
func OpenFluxShareDecode(link *C.char) *C.char {
	if link == nil {
		return shareJSON(shareResult{Error: "empty link"})
	}
	cfg, err := share.Decode(C.GoString(link))
	if err != nil {
		return shareJSON(shareResult{Error: err.Error()})
	}
	return shareJSON(shareResult{Config: &cfg})
}

// OpenFluxShareEncode собирает ссылку из JSON-описания конфигурации и
// возвращает {"link":"openflux://v1/..."} либо {"error":"..."}. Валидация — та
// же, что в CLI, поэтому невалидную ссылку приложение не выпустит.
//
//export OpenFluxShareEncode
func OpenFluxShareEncode(cfgJSON *C.char) *C.char {
	if cfgJSON == nil {
		return shareJSON(shareResult{Error: "empty config"})
	}
	var cfg share.Config
	if err := json.Unmarshal([]byte(C.GoString(cfgJSON)), &cfg); err != nil {
		return shareJSON(shareResult{Error: "bad config json: " + err.Error()})
	}
	link, err := share.Encode(cfg)
	if err != nil {
		return shareJSON(shareResult{Error: err.Error()})
	}
	return shareJSON(shareResult{Link: link})
}
