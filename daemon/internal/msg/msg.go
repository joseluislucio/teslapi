// Package msg define los mensajes que el daemon enseña en el portal. No llevan
// texto: un código y sus datos, que el portal traduce al idioma de quien mira.
// Detail lleva el texto técnico del fallo (salida de mount, de nmcli…), sin traducir.
package msg

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Msg struct {
	Code   string         `json:"code"`
	Args   map[string]any `json:"args,omitempty"`
	Detail string         `json:"detail,omitempty"`
}

// New crea un mensaje; kv son pares clave, valor.
func New(code string, kv ...any) Msg {
	m := Msg{Code: code}
	for i := 0; i+1 < len(kv); i += 2 {
		if m.Args == nil {
			m.Args = map[string]any{}
		}
		m.Args[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return m
}

func (m Msg) WithDetail(err error) Msg {
	if err != nil {
		m.Detail = err.Error()
	}
	return m
}

func (m Msg) IsZero() bool { return m.Code == "" && m.Detail == "" }

// String es la versión para el registro: código, datos ordenados y detalle.
func (m Msg) String() string {
	var b strings.Builder
	b.WriteString(m.Code)
	keys := make([]string, 0, len(m.Args))
	for k := range m.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, m.Args[k])
	}
	if m.Detail != "" {
		b.WriteString(": " + m.Detail)
	}
	return b.String()
}

// UnmarshalJSON acepta también un texto suelto (historial guardado por versiones
// anteriores), que pasa a Detail.
func (m *Msg) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*m = Msg{Detail: s}
		return nil
	}
	type plain Msg
	return json.Unmarshal(b, (*plain)(m))
}

// Error es un error con código, para que la API lo devuelva traducible.
type Error struct{ Msg }

func (e *Error) Error() string { return e.Msg.String() }

func Err(code string, kv ...any) *Error { return &Error{New(code, kv...)} }

func Wrap(code string, err error, kv ...any) *Error {
	return &Error{New(code, kv...).WithDetail(err)}
}
