// Package shell ejecuta herramientas del sistema (nmcli, losetup, mount...) con
// el error y la salida juntos, para que el motivo de un fallo llegue al portal.
package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sensitive son los nombres de argumento cuyo valor es secreto. Se comparan enteros, nunca
// por subcadena: decidir por lo que contiene el valor filtra una clave como "MiPassword1".
var sensitive = map[string]bool{
	"wifi-sec.psk": true, "802-11-wireless-security.psk": true,
	"wifi-sec.wep-key0": true, "802-1x.password": true, "password": true, "psk": true,
}

// redact oculta el valor que sigue a un nombre sensible (nmcli: "wifi-sec.psk <clave>").
func redact(args []string) []string {
	out := make([]string, len(args))
	hide := false
	for i, a := range args {
		switch {
		case hide:
			out[i], hide = "***", false
		default:
			out[i], hide = a, sensitive[strings.ToLower(a)]
		}
	}
	return out
}

// trace anota cada orden en el fichero de TESLAPI_TRACE y lo fuerza a disco ANTES de
// ejecutarla: si la Pi se cuelga, la última línea dice qué estaba haciendo.
func trace(name string, args []string) {
	path := os.Getenv("TESLAPI_TRACE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	parts := append([]string{name}, redact(args)...)
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), strings.Join(parts, " "))
	f.Sync()
}

func Run(ctx context.Context, name string, args ...string) (string, error) {
	trace(name, args)
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(redact(args), " "), err, msg)
	}
	return out.String(), nil
}

// ExitCode devuelve el código de salida de un error de Run (-1 si no lo hay).
func ExitCode(err error) int {
	var ee *exec.ExitError
	for e := err; e != nil; {
		if x, ok := e.(*exec.ExitError); ok {
			ee = x
			break
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	if ee == nil {
		return -1
	}
	return ee.ExitCode()
}
