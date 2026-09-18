// Package text holds the words the notch shows, in Portuguese or English after $LANG, and the
// formatting of percentages and times.
package text

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
)

type Lang string

const (
	PT Lang = "pt"
	EN Lang = "en"
)

// Detect picks the language from LC_ALL, LC_MESSAGES or LANG, in that order.
func Detect() Lang {
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := os.Getenv(v); l != "" {
			if strings.HasPrefix(l, "pt") {
				return PT
			}
			return EN
		}
	}
	return EN
}

var table = map[string][2]string{ // key → {pt, en}
	"usage":           {"Uso", "Usage"},
	"refresh":         {"Atualizar agora", "Refresh now"},
	"open":            {"Abrir %s", "Open %s"},
	"install_hooks":   {"Instalar hooks do Claude Code", "Install Claude Code hooks"},
	"uninstall_hooks": {"Remover hooks do Claude Code", "Remove Claude Code hooks"},
	"quit":            {"Sair do gonotch", "Quit gonotch"},
	"resetting":       {"Reiniciando…", "Resetting…"},
	"resets_in":       {"Reinicia em %s", "Resets in %s"},
	"resets_at":       {"Reinicia %s", "Resets %s"},
	"used_left":       {"%s%% usado · %s%% restante", "%s%% used · %s%% left"},
	"updated":         {"Atualizado %s", "Updated %s"},
	"loading":         {"Lendo…", "Reading…"},
	"running":         {"trabalhando", "working"},
	"attention":       {"esperando você", "waiting on you"},
	"done":            {"concluída", "done"},
	"idle":            {"parada", "idle"},
	"sessions":        {"Sessões", "Sessions"},
	"jump_failed":     {"Não achei a janela do terminal", "Could not find the terminal window"},
	"settings":        {"Configurações…", "Settings…"},
	"settings_title":  {"Configurações", "Settings"},
	"providers":       {"Provedores", "Providers"},
	"providers_hint":  {"Desligado, o provedor some do notch e não consulta mais a API.", "A provider switched off leaves the notch and stops calling its API."},
	"position":        {"Posição", "Position"},
	"edge":            {"Borda", "Edge"},
	"left":            {"Esquerda", "Left"},
	"right":           {"Direita", "Right"},
	"height":          {"Altura", "Height"},
	"top":             {"topo", "top"},
	"middle":          {"meio", "middle"},
	"bottom":          {"base", "bottom"},
	"hide":            {"Ocultar", "Hide"},
	"autohide":        {"Auto-ocultar", "Auto-hide"},
	"autohide_hint":   {"Recolhe numa faixa fina na borda; encoste o mouse nela para abrir.", "Tucks into a thin strip on the edge; touch it with the pointer to open."},
	"not_installed":   {"Não instalado neste computador", "Not installed on this computer"},
	"off":             {"Desligado", "Off"},
	"stale":           {"desatualizado", "stale"},
	"move_up":         {"Subir", "Move up"},
	"move_down":       {"Descer", "Move down"},
	"show_ring":       {"Mostrar no notch", "Show in the notch"},
}

// T looks a key up; unknown keys come back as themselves.
func T(l Lang, key string) string {
	v, ok := table[key]
	if !ok {
		return key
	}
	if l == PT {
		return v[0]
	}
	return v[1]
}

var labelsPT = map[string]string{
	"Current session":       "Sessão atual",
	"Weekly (all models)":   "Semanal (todos os modelos)",
	"Weekly (Opus)":         "Semanal (Opus)",
	"Weekly (model-scoped)": "Semanal (por modelo)",
	"Weekly limit":          "Limite semanal",
	"Monthly limit":         "Limite mensal",
	"Longer window":         "Janela longa",
	"Included usage":        "Uso incluído",
	"API usage":             "Uso de API",
	"On demand":             "Sob demanda",
	"Code review":           "Revisão de código",
	"Premium requests":      "Requisições premium",
	"Chat requests":         "Requisições de chat",
	"Code completions":      "Completions de código",
}

var lengthLabel = regexp.MustCompile(`^(\d+)([mhd]) limit$`)

// Label translates a provider's window label; the providers name windows in English.
func Label(l Lang, label string) string {
	if l != PT {
		return label
	}
	if pt, ok := labelsPT[label]; ok {
		return pt
	}
	if m := lengthLabel.FindStringSubmatch(label); m != nil {
		return "Limite de " + m[1] + map[string]string{"m": "min", "h": "h", "d": " dias"}[m[2]]
	}
	return label
}

// Pct is the ring's number: whole percents, except where rounding would read as nothing used.
func Pct(used float64) string {
	v := used * 100
	if v > 0 && v < 1 {
		return small(v) + "%"
	}
	return fmt.Sprintf("%d%%", int(math.Round(v)))
}

func small(v float64) string {
	if v <= 0 {
		return "0"
	}
	t := math.Round(v*10) / 10
	switch {
	case t < 0.1:
		return "<0.1"
	case t > 99.9:
		return ">99.9"
	}
	return fmt.Sprintf("%.1f", t)
}

// UsedLeft is "12% used · 88% left", with the left half derived from the rounded used half, as
// the vendors' dashboards do.
func UsedLeft(l Lang, used float64) string {
	v := used * 100
	var u, left string
	if (v > 0 && v < 1) || (v > 99 && v < 100) {
		u, left = small(v), small(max(0, 100-v))
	} else {
		r := int(math.Round(v))
		u, left = fmt.Sprint(r), fmt.Sprint(max(0, 100-r))
	}
	return fmt.Sprintf(T(l, "used_left"), u, left)
}

var weekdays = map[Lang][7]string{
	PT: {"dom", "seg", "ter", "qua", "qui", "sex", "sáb"},
	EN: {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
}

// Reset says when a window resets: a countdown under a day, a weekday and time within a week, a
// date after that.
func Reset(l Lang, at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := at.Sub(now)
	if d <= 0 {
		return T(l, "resetting")
	}
	if d < 24*time.Hour {
		return fmt.Sprintf(T(l, "resets_in"), Duration(d))
	}
	local := at.Local()
	when := fmt.Sprintf("%s %s", weekdays[l][local.Weekday()], local.Format("15:04"))
	if d >= 7*24*time.Hour {
		when = local.Format("02/01 15:04")
		if l == EN {
			when = local.Format("Jan 2 15:04")
		}
	}
	return fmt.Sprintf(T(l, "resets_at"), when)
}

// Duration is "2h 13min", "45min", "<1min". Minutes are rounded before anything else, so 59m40s
// never reads as "60min".
func Duration(d time.Duration) string {
	m := int(math.Round(d.Minutes()))
	switch {
	case m < 1:
		return "<1min"
	case m < 60:
		return fmt.Sprintf("%dmin", m)
	case m%60 == 0:
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dmin", m/60, m%60)
}

// Ago is "há 3 min" / "3 min ago".
func Ago(l Lang, d time.Duration) string {
	s := Duration(d)
	if l == PT {
		return "há " + s
	}
	return s + " ago"
}
