#!/bin/bash
# l_tess_wm.sh — автозапуск wm-tview в ЛЮБОМ терминале, развёрнутом на
# рабочую область (не перекрывая панель).
#
# Настройка — конфиг лежит РЯДОМ со скриптом: <каталог скрипта>/l_tess_wm.conf
# (переменные-строки):
#   TESS_TERMINAL_CMD="xterm"    # примеры ниже
#   TESS_WIN_TITLE="TessWorkspace"
#   TESS_WORKAREA=1     # 1 = раскрыть на рабочую область без панели (WA/EWMH)
#                       # 0 = обычная максимизация на весь экран
# Если TESS_TERMINAL_CMD не задан — берётся первый найденный терминал из
# списка: xfce4-terminal, alacritty, kitty, xterm. Примеры конфига
# (в l_tess_wm.conf рядом со скриптом):
#
#   TESS_TERMINAL_CMD='xfce4-terminal -T TessWorkspace -e "me tess_wm"'
#   TESS_TERMINAL_CMD='alacritty -t TessWorkspace -e me tess_wm'
#   TESS_TERMINAL_CMD='kitty --title TessWorkspace me tess_wm'
#   TESS_TERMINAL_CMD='xterm -title TessWorkspace -e me tess_wm'
#
# Требование: для разворота нужен wmctrl (без него окно просто откроется).
set -u

# Гарантируем, что me доступен даже в среде автостарта
export PATH="$HOME/.local/bin/me:$HOME/.local/bin:$PATH"

# --- каталог скрипта (конфиг ищем рядом со скриптом) -----------------------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONF="${TESS_CONF:-$SCRIPT_DIR/l_tess_wm.conf}"

# --- конфигурация ---------------------------------------------------------
TESS_WIN_TITLE="${TESS_WIN_TITLE:-TessWorkspace}"
TESS_WORKAREA="${TESS_WORKAREA:-1}"

if [ -f "$CONF" ]; then
    set -a
    # shellcheck disable=SC1090
    . "$CONF"
    set +a
elif [ -f "$HOME/.config/wm-tview/autostart.conf" ]; then
    # обратная совместимость со старым местом конфига
    set -a
    # shellcheck disable=SC1090
    . "$HOME/.config/wm-tview/autostart.conf"
    set +a
else
    echo "[l_tess_wm] конфиг не найден: $CONF (или ~/.config/wm-tview/autostart.conf)" >&2
    echo "[l_tess_wm] создайте $CONF с переменной TESS_TERMINAL_CMD — см. шапку скрипта." >&2
fi

if [ -z "${TESS_TERMINAL_CMD+x}" ]; then
    for t in xfce4-terminal alacritty kitty xterm; do
        if command -v "$t" >/dev/null 2>&1; then
            case "$t" in
                xfce4-terminal) TESS_TERMINAL_CMD="xfce4-terminal -T '$TESS_WIN_TITLE' -e 'me tess_wm'" ;;
                alacritty)      TESS_TERMINAL_CMD="alacritty -t '$TESS_WIN_TITLE' -e me tess_wm" ;;
                kitty)          TESS_TERMINAL_CMD="kitty --title '$TESS_WIN_TITLE' me tess_wm" ;;
                xterm)          TESS_TERMINAL_CMD="xterm -title '$TESS_WIN_TITLE' -e me tess_wm" ;;
            esac
            break
        fi
    done
fi

if [ -z "${TESS_TERMINAL_CMD+x}" ]; then
    echo "[l_tess_wm] не найден ни один терминал (xfce4-terminal/alacritty/kitty/xterm)" >&2
    exit 1
fi

# Запускаем терминал с wm-tview в фоне (отвязываем, чтобы не убился при выходе).
# Обратите внимание: команда берётся из конфига пользователя.
eval "$TESS_TERMINAL_CMD" &
disown 2>/dev/null || true

# --- ожидание окна и разворот --------------------------------------------
if command -v wmctrl >/dev/null 2>&1; then
    wid=""
    for _ in $(seq 1 40); do
        wid=$(wmctrl -l | awk -v t="$TESS_WIN_TITLE" 'index($0, t) {print $1; exit}')
        [ -n "$wid" ] && break
        sleep 0.1
    done
    if [ -n "$wid" ]; then
        if [ "$TESS_WORKAREA" = "1" ]; then
            # Уточнение: точная геометрия рабочей области без панелей (WA/EWMH).
            # Формат "WA: X,Y WxH" — на пробелы заменяем и запятые, и 'x'.
            wa=$(wmctrl -d | awk '/\*/ && index($0,"WA:") {t=substr($0,index($0,"WA:")+3); gsub(/[,x]/, " ", t); print t}' | head -1)
            set -- $wa
            if [ "${4:-0}" -gt 0 ] 2>/dev/null; then
                wmctrl -i -r "$wid" -e 0,"$1","$2","$3","$4"
            fi
        fi
        # Основной «разворот» (EWMH-максимизация): надёжно на всех WM и
        # не перекрывает панель.
        wmctrl -i -r "$wid" -b add,maximized_vert,maximized_horz
        wmctrl -i -a "$wid"   # на передний план
    fi
fi