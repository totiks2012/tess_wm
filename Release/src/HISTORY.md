# HISTORY.md — история проекта wm-tview

## 2026-10-20 21:40 — [FIX] Скролл клавишами PgUp/PgDn (баг «Shift+PgUp не работает, колесо — работает»)
- Что: Пользователь сообщил: скролл через колесо мыши работает, а `Shift+PgUp`/`Shift+PgDn` — нет. Причина — **никто из реальных терминалов не доставляет в приложение модификатор Shift для PageUp**:
  - alacritty по умолчанию биндит `Shift+PageUp/Down` на прокрутку собственного scrollback (невидимого, пока приложение владеет альтернативным экраном) — событие вообще не доходит до app;
  - kitty (`\E[338;2u`) и xterm/VTE (`\E[27;2;57358~`) перекодируют Shift+PgUp в последовательности, которые tcell не отображает обратно в `KeyPgUp`; ветка `event.Key()==KeyPgUp && ModShift` никогда не срабатывает.
  Колёса мыши не зависит от терминала (приходит как tcell-событие мыши) — поэтому работает, а комбинация клавиш — нет.
- Где: `prototype/shell_window.go` — `handleScrollKey` переписан: принимает **обычные** `PgUp`/`PgDn` (+ с Shift, если терминал доносит), `Alt+k/j` по строкам; `prototype/main.go` — справка базы. Новый `hasScrollback()`.
- Решение: обычный PageUp — надёжный кросс-терминальный сигнал (приходит как чистый `KeyPgUp` везде). Политика:
  - **alt-screen (less/htop/vim в окне) → PgUp/PgDn уходят внутрь программы** (её собственные клавиши), скролл окна — как раньше;
  - **нет scrollback → PgUp/PgDn прозрачно уходят в shell** (сохранён readline `PgUp = history-search-backward` на свежем пустом окне);
  - **обычный промпт/вывод + есть scrollback → PgUp/PgDn скроллят вьюпорт окна** (как колесо).
- Проверки: `go build/vet` чисто; `gofmt` чисто; `go test -race -count=1 ./...` — **21/21 PASS**: +`TestPlainPgUpScrollsAltScreenAndEmptySheet` (пустой лист → passthrough, scrollback → скролл, Shift+PgDn → вниз, alt-screen → passthrough; старый `TestScrollKeysAndStickyBottom` продолжает проходить). Бинарь пересобран: `~/.local/bin/wm-tview`.
- Нюансы: у пользователя на alacritty уникально — сама комбинация `Shift+PgUp` физически съедается эмулятором и app её не увидит никогда (нужно доп. действие: убрать биндинг в alacritty.toml либо пользоваться обычным PgUp/колесом). `top` из procps не использует alt-screen — при нём PgUp/Dn скроллят окно (известно с 5.1).
- Следующий: Этап 6 (метод как окно) — по факту ежедневного использования.

## 2026-10-20 20:25 — [FEAT] Scrollback, max-геометрия, адаптивная мышь (SEED_to_scrol 19.10)
- Что: Шлифовка `tess_wm` по SEED: (1) просмотр истории вывода через VT-scrollback, (2) максимизация окна с отступом 1 ячейка, (3) адаптивная мышь (TTY vs GUI).
- Где: `prototype/shell_window.go`, `prototype/wm.go`, `prototype/overview.go`, `prototype/main.go`, новый `prototype/tty.go`, тесты (`shell_window_test.go`, `overview_test.go`, новые `tty_test.go`), пересборки `~/.local/bin/wm-tview`.
- Решения:
  - **Спайк (S0):** `charmbracelet/x/vt@v0.1.0` НЕ даёт отрицательных Y в `CellAt`; scrollback — отдельный буфер: `emo.ScrollbackLen()`, `emo.ScrollbackCellAt(x, y)` (y = индекс истории, 0 = старейший), `emo.SetScrollbackSize(n)` — все concurrency-safe на `*SafeEmulator`. Блокер SEED «если API нет — стоп» снят; модель координат адаптирована.
  - **S1 (скролл-вьюпорт):** `scrollOff` в `shellWindow` (под `w.mu`); при `scrollOff>0` `Draw` рисует поток (scrollback над экраном) через единый `streamCellAt(x, vy)` (vy≥0 = экран, vy<0 = `ScrollbackCellAt(sbLen+vy)`); scrollback 200→**2000** строк. Хоткеи: **Shift+PgUp/PgDn** (полстраницы), **Alt+k/j** (по строке). **Sticky bottom:** любой ввод при `scrollOff>0` сбрасывает вьюпорт вниз.
  - **S2 (copy-mode в истории):** координаты copy-курсора/выделения переведены на виртуальные Y (отрицательные = scrollback); `PgUp/PgDn/↑↓` уходят в историю, вьюпорт автопрокручивается за курсором (`ensureCopyVisible`, как less); `normalizeSel`/`selectedTextLocked`/`drawCopyOverlay` читают обе области. Вход в copy-mode сбрасывает `scrollOff=0`; выход возвращает к live.
  - **S3 (колесо мыши):** в `MouseHandler` shell — `tview.MouseScrollUp/Down` → `scrollBy(±3)` (это `tview.MouseAction`, не `tcell.ButtonMask` — SEED-блокер подтверждён); в Overview — колесо двигает выделение, **клон по миниатюре** — вход (S6).
  - **S4 (геометрия):** `wm.go` `mw,mh w*8/10,h*8/10 → w-2,h-2; mx,my=x+1,y+1` + clamp на 1 ячейку (защита от паники при малых размерах).
  - **S5 (TTY-детект):** `isGUITerminal()` — WAYLAND_DISPLAY ⇒ GUI; DISPLAY + `readlink /proc/self/fd/0` не `/dev/ttyN` ⇒ GUI; иначе консоль. В `main.go`: `if isGUITerminal() && termSupportsMouse()` (терминfo `ti.Mouse`), `app.EnableMouse(true)`; в чистой TTY мышь не включается вообще. tview v0.42 не имеет `GetScreen()` (SEED опирался на другой API) — фильтр `HasMouse()` имитирован через `terminfo.LookupTerminfo(TERM).Mouse`.
  - **guard в main:** при ошибке `app.Run()` больше НЕ зовём `app.Stop()` (иначе tcell `close(nil)` panic на неинициализированном экране — воспроизведено в pty без controlling tty; на реальном терминале не возникает, но guard правильный). Справка базы обновлена (Shift+Alt скролл, клик по миниатюре).
- Проверки: `go build/vet` чисто; `go test -race -count=1 ./...` — **19/19 PASS**: +`TestScrollbackCellAccess` (спайк S0), +`TestScrollKeysAndStickyBottom`, +`TestMouseWheelScrollsScrollback`, +`TestScrollbackViewportRendering`, +`TestOverviewMouseClickActivates`, +`TestOverviewMouseWheelMovesSelection`, +`TestTTYConsoleDevice`, +`TestIsGUITerminalWayland`, +`TestIsGUITerminalNoDisplay`, +`TestGUIBYEnvTable`, +`TestTermSupportsMouse` (существующие copy-mode тесты переведены на виртуальные Y и проходят). pty-смок `/tmp/opencode/sb/smoke_sb.py` (реальный бинарь под `script`): **10/10 PASS** — старт, F2-shell, вывод seq, Alt+k скролл, sticky bottom (echo выполняется после скролла), copy-mode PgDn, Ctrl-D закрытие окна, Ctrl-C выход, нет зомби.
- Нюансы: в чистой TTY (без DISPLAY/WAYLAND) мышь и не опрашивается (A5); в этом окружении `DISPLAY=:0.0` + TERM xterm — мышь включилась корректно (GUI). `top` из procps не использует alt-screen — при нём скролл-клавиши работают, F-клавиши ведут себя как на промпте (известно с 5.1).
- Следующий: Этап 6 (метод как окно) — по факту ежедневного использования.

## 2026-10-16 14:10 — [FIX][DECISION] Разбор внешней критики: только один пункт из четырёх содержал реальное зерно
- Что: Пришёл внешний обзор кода с 4 замечаниями. Проверен каждый по исходникам (vt@v0.1.0, реальный код), исправлено одно:
  - **1. «Дедлок w.mu↔wm.mu» — зерна нет.** Уже соблюдено: колбэки `onExit`/`wantsRedraw` вызываются без удержания блокировок (`notifyOutput` после `w.mu.Unlock()`; `shouldRedraw` зовёт `wantsRedraw` до `w.mu.Lock()`), существует только порядок `wm.mu→w.mu`, обратного пути нет (это чинилось в 4.5).
  - **2. «io.Copy(ptmx, emu) зависит от внутреннего типа pipe» — зерна нет.** Проверено: `vt.SafeEmulator` НЕ защищает `Read`/`Close`, а `Emulator.Read` читает поле `e.closed`, которое пишет `Close()` → вызов `emu.Close()` дал бы data race (-race). Разблокировать копировальщик можно только закрыв stdlib `io.Pipe` (тип `*io.PipeWriter` стабилен, эмулятор.go:101). Это намеренный обход, не «костыль».
  - **3. «Спам буфера обмена из внешних процессов» — посыл ложный (операции только по жесту пользователя, уже async), но НАЙДЕНО зерно:** порядок инструментов. На Wayland с установленными xclip/xsel `setClipboard` проваливал до 4 таймаутов по 500 мс (до ~2 с на копирование), пока дойдёт до `wl-copy`; чтение же всегда начинало с `wl-paste` даже на X11. **Исправлено**: `waylandSession()` (`WAYLAND_DISPLAY`) → `clipboardReadTools`/`clipboardWriteTools` в порядке активной сессии.
  - **4. «`set -- $wa` упадёт по set -u» — заявление неверно.** `${4:-0}` уже защищает от unset; gsub убирает `,` и `x` → в `$wa` только цифры/пробелы, word-splitting безопасен. Массивы — стилистика, не исправление.
- Где: `prototype/shell_window.go` (`waylandSession`, `clipboardReadTools`, `clipboardWriteTools` вместо трёх хардкод-списков; `primarySelection`/`clipboardSelection`/`setClipboard` переведены на порядок по сессии).
- Проверки: `gofmt` чисто; `go vet`/`go build` ok; `go test -race -count=1 ./...` — **24/24 PASS** (+2: `TestClipboardOrderingBySession` — wl-paste побеждает xclip на Wayland и наоборот на X11; `TestClipboardWriteOrderBySession` — wl-copy первым на Wayland при живом xclip). Бинари пересобраны: `/root/.local/bin/wm-tview`, `/root/DEV/tess/tess_wm`, `prototype/prototype`.
- Следующий: Этап 6 (метод как окно) — по факту ежедневного использования; либо докрутки из списка (README автостарта, HISTORY l_tess_wm, инцидент xfce4-terminal -e).

## 2026-10-16 07:50 — [FEAT] Клавиатурное выделение (copy mode, F5) + Ctrl+Insert копировать / Shift+Insert вставить
- Что: Пользователь попросил выделение/копирование с клавиатуры в субтерминале («как выделять?»). Без copy mode выделить текст в VT-эмуляторе нельзя — добавлен tmux-стиль **copy mode (F5)**: стрелки/hjkl — курсор, PgUp/PgDn, Home/End; **Space** — закрепить якорь выделения; **Enter** — скопировать прямоугольник в буфер и выйти; **Esc/q/F5** — выйти без копирования. Во время режима над окном рисуется подсказка + выделение инвертируется (reverse), pty-курсор скрывается, в заголовке окна появляется метка `COPY`. После выхода **Ctrl+Insert** повторно копирует последнее выделение, **Shift+Insert** вставляет CLIPBOARD (fallback — PRIMARY). Как и раньше, средняя кнопка вставляет PRIMARY.
- Где: `prototype/shell_window.go` (поля copy-mode под `w.mu`; методы `ToggleCopyMode`/`handleCopyModeKey`/`moveCopyCursor`/`commitSelection`/`selectedTextLocked`/`drawCopyOverlay`/`copySelection`/`pasteClipboard`; `InputHandler` перехватывает Ctrl+Insert/Shift+Insert и ключи режима; `Draw` рисует выделение+подсказку); `prototype/wm.go` (`ToggleCopyMode`, метка COPY в заголовке); `prototype/main.go` (клавиша F5, обновлён help).
- Решения:
  - Бинды: copy mode — F5 (по политике F-клавиш: на промпте F5 — WM, внутри полноэкранной TUI F5 уходит в программу); копия — Ctrl+Insert; вставка — Shift+Insert. tcell парсит `CSI 2;5~`/`2;2~` в KeyInsert+ModCtrl/ModShift — подтверждено в input.go.
  - Вставка работает и через новый `writeTextToPty` (нормализация `\n`→`\r`, как у middle-click).
  - Выделение фиксируется в экранных координатах (вывод бежит и под ним) — известное ограничение, как в tmux.
- Проверки: `go build/vet` ok; `go test -race -count=1 ./...` — **21/21 PASS** (+6: `TestCopyModeSelectionAndCopy` — полный путь выделения с настоящим выделением «hello» и стабом xclip в изолированном песке PATH; `TestCopyModeEnterWithoutSelection`; `TestCopyModeQExits`; `TestCtrlInsertRecopiesSelection`; `TestShiftInsertPastesClipboard` — реальный bash исполняет вставку из CLIPBOARD; `TestWMToggleCopyMode`). Бинари пересобраны в `/root/.local/bin/tess/tess_wm` и `~/.local/bin/wm-tview` (md5 совпадают). Зомби/следов нет.
- Нюанс перезапуска: запущенный в 07:12 старый `tess_wm` (pid 8597) не содержит новых хоткеев — для проверки закрыть и открыть заново (`me tess_wm`).
- Следующий: Этап 6 (метод как окно / меню методов) — по факту ежедневного использования.

## 2026-10-16 07:10 — [FIX] Средняя кнопка мыши не вставляла PRIMARY-выделение в субтерминал
- Что: Пользователь сдал баг: средняя кнопка мыши в подокне-терминале не вставляет выделение (вместо вставки ничего/мусор). Причина: tview по умолчанию включает tcell mouse tracking (`\x1b[?1000h...h`), и любой современный терминал (xterm/alacritty/kitty) в этом режиме гасит НАТИВНУЮ вставку PRIMARY по средней кнопке и шлёт событие кнопки приложению — а приложение со средней кнопкой ничего не делало.
- Где: `prototype/shell_window.go` (новые `MouseHandler`, `pastePrimary`, `normalizePasteNewlines`, `primarySelection`, `runSelectionTool`); `prototype/wm.go` (`MouseHandler` теперь уважает открытый F9-обзор — не вставляет в окно под оверлеем).
- Решение: при `MouseMiddleDown`/`MouseMiddleClick` внутри окна shell читаем X11/Wayland PRIMARY («вторичный буфер») через `xclip -o -selection primary` / `xsel -o --primary` / `wl-paste --primary` (таймаут 500 мс, инструмент ищется по PATH) и вводим текст в pty. Переводы строк нормализуются в `\r` (Enter), как делает терминал при вставке. Чтение буфера — в горутине (дисплейный сервер может подвиснуть). Средняя кнопка вне окна и вне shell (виджеты, обзор) поведение не меняет.
- Зависимости: нужен один из `xclip`/`xsel` (X11) или `wl-paste` (Wayland); если ни одного нет — вставка по средней кнопке молча не срабатывает (внешняя вставка Ctrl+V / штатное действие терминала не зависят от этого).
- Проверки: `go build/vet` ok; `go test -race -count=1 ./...` — **15/15 PASS** (+3 новых: `TestShellWindowMiddlePasteHandler` — средний клик внутри окна consumed, вне — нет, левый клик сохраняет Box-поведение; `TestNormalizePasteNewlines`; `TestShellWindowPastePrimaryEndToEnd` — реальный bash + стаб `xclip` в изолированном tmpdir-песке через подмену PATH (правило vtest), вставка выполняется bash, результат виден в VT-буфере). Нюанс теста: wantsRedraw=false, т.к. без event loop tview `QueueUpdateDraw` валсил бы pty-read горутину (артефакт харнесса, в работающем приложении канал дрейнится). Бинарь пересобран в `~/.local/bin/wm-tview`. Зомби/следов нет.
- Следующий: Этап 6 (метод как окно / меню методов в TUI) — по факту ежедневного использования; либо эксплуатация по правилу «месяц без использования — остановиться».

## 2026-10-15 23:20 — [FIX][DECISION] Скроллбэк VT-эмулятора ограничен (память при флуде вывода)
- Что: Замер памяти выявил: при непрерывном выводе (yes/tail/log) RSS рос неограниченно — `charmbracelet/x/vt` по умолчанию держит **scrollback на 10000 строк** и клонирует каждую ушедшую строку. Мы рендерим только видимый экран и не имеем внутриоконного скролла, так что scrollback — чистый расход.
- Где: `prototype/shell_window.go` (в `newShellWindow` после создания эмулятора: `w.emu.SetScrollbackSize(200)`; потолка в 0 не даёт `SetMaxLines` guard `<=0`).
- Проверки (замер RSS через /proc): idle — 7 MB (база), +1-2 MB на каждое окно; **флуд `yes` 10 с — плато 15-16 MB** (1 окно), ранее рос без потолка; 4 окна при флуде — ~30 MB и тоже плато (вновь: было бы больше, dialog с 10000-строками на окно). Бинарь пересобран (`prototype/`, `~/.local/bin/tess/tess_wm`); `go test -race` ok.
- Следующий: при желании жёсткий потолок heap — запускать с `GOMEMLIMIT=64MiB` (Go 1.26 soft limit гарантирует границу независимо от GC).

## 2026-10-15 23:55 — [FIX][FEAT] F-клавиши: контекстно по alt-screen + ручной режим F12
- Что: Пользователь сдал баг: при открытом shell и запущенном процессе F2 (новое окно) и F4 (закрыть) не работают. Это было прямым следствием решения 4.5 (при фокусе shell Fn уходят внутрь). Решение — контекст по alt-screen (автоматика) + F12 как ручной переключатель режима.
- Где: `prototype/shell_window.go` (метод `altScreen()`), `prototype/wm.go` (`activeShellAltScreen`, `ToggleFKeyMode`/`shouldPassFKey`/`FKeyModeLabel`), `prototype/main.go` (gate через `shouldPassFKey`, перехват F12, справка), `prototype/shell_window_test.go` (+`TestF12ModeToggle`), `/tmp/opencode/smoke6.py`.
- Решения:
  - **auto** (по умолчанию): F-клавиши — команды WM на промпте/обычном выводе/в базах/виджетах; уходят в программу, только когда внутри окна запущена полноэкранная TUI (в VT-эмуляторе включён alt-screen — htop/mc/vim/less). Это чинит баг: при `make`/`sleep`/любом обычном процессе F2/F4 снова управляют окнами, а htop/vim не теряют F-клавиши (4.5 сохранён).
  - **F12 — тумблер**: auto ↔ WM-force (F-клавиши всегда команды WM, даже внутри htop — управляешь окнами из полноэкранной программы). F12 перехватывается WM всегда и внутрь программы никогда не уходит.
  - Метка режима в заголовке окна: `· F12: auto | F12: WM`; в справке базы добавлена строка про F12.
  - Нюанс: проверено на реальных прогах — htop/less/mc шлют `\x1b[?1049h` (детект работает), а procps `top` здесь alt-screen НЕ использует (терминал через обычный буфер), поэтому top ведёт себя как «промпт» → F2/F4 при top управляют окнами (не критично: F-клавиши top — лишь альтернатива одиночным буквам).
- Проверки: `go build/vet`; `go test -race -count=1 ./...` — **11/11 PASS** (+`TestF12ModeToggle`), регрессии: pty-смок `smoke5` (обзор) 16/16, `smoke6` (F-клавиши: auto-промпт/htop + F12) 12/12, `smoke45` переведён на новую политику — 15/15; зомби и следов нет.
- Следующий: эксплуатация; Этап 6 — только при ежедневном использовании `me wm`.

## 2026-10-15 23:10 — [FEAT] Этап 5 завершён: модальный F9-обзор с живыми миниатюрами всех окон
- Что: Визуальный переключатель окон (mission control): F9 открывает модальный оверлей с сеткой живых миниатюр всех открытых окон; ↑↓←→/hjkl перемещают выделение, Enter активирует выбранное окно (фокус/ввод переходят в него, обзор закрывается), Esc/F4/F9 закрывает без изменений.
- Где: новый `prototype/overview.go`; `prototype/wm.go` (`ovf`, `OpenOverview`/`CloseOverview`/`overviewOpen`/`activateOverview`, Draw+InputHandler маршрутизация), `prototype/shell_window.go` (переименован `isActive`→`wantsRedraw`, добавлен `renderScaled`), `prototype/main.go` (F9).
- Решения:
  - **Держаться формы «B»** (модальный оверлей) по выбору пользователя; F9 следует стандартному gate 4.5: при фокусе shell обычный F9 уходит внутрь (mc/htop им пользуются), обзор открывается Alt+F9; из базы/виджета — обычный F9.
  - Транзитентность: обзор НЕ входит в `wm.wins` → не пишется в сессию, не участвует в Alt+F3-цикле, не виден в своей собственной сетке (нет рекурсии R-OV1).
  - Миниатюры = nearest-выборка VT-буфера (`renderScaled`; `emu.CellAt` под `w.mu`) — pty в миниатюрах НЕ ресайзятся, SIGWINCH только при Enter-раскрытии (R-OV3/ресайз).
  - Троттлинг 4.5 переключается локально: `wantsRedraw = wm.isActiveWindow(sw) || wm.overviewOpen()` — пока оверлей открыт, перерисовываются все окна (миниатюры живые), после закрытия — только активное. Это и есть «перекрывание» фонового троттлинга в обзоре.
  - `QueueUpdateDraw` в `activate()`/`close()` оверлея НЕ вызывается: tview рисует после каждого keyhandler (иначе юнит-тест без event loop завис бы на `QueueUpdate`).
  - Сетка: `cols=ceil(sqrt(N))`, min-ячейка ≥12×6; при переполнении/нехватке места — уменьшенная сетка с нотой «…ещё N» либо fallback-«лист» (`ovLayout`). Подсветка: активное — синяя, выделенное — жёлтая рамка.
- Проверки: `go build/vet`; `go test -race -count=1 ./...` — **10/10 PASS** (+ `TestOverviewLiveThumbnails` на реальных bash-маркерах QQ/ZZ, `TestOverviewSelectedHighlightsActive`, `TestOverviewOverflowMeasures`); pty-смок `/tmp/opencode/smoke5.py` — **16/16 PASS ×3**; зомби и фоновых следов нет; порядок ввода подтверждён маркерами в соответствующих окнах.
- Следующий: Этап 6 (метод как окно / меню методов в TUI) — только при ежедневном использовании `me wm`; иначе эксплуатация по SEED-правилу.

## 2026-10-15 22:00 — [FIX][DECISION] Этап 4.5: критические баги из ревью (гонка данных, троттлинг, F-клавиши)
- Что: Внешняя критика выявила 3 проблемы. Проверено зерно истины — все три реальные.
- Где: `prototype/shell_window.go`, `prototype/wm.go`, `prototype/main.go`, новый `prototype/shell_window_test.go`; бинарь пересобран в `prototype/prototype` и `~/.local/bin/wm-tview`.
- **Ч1 [CRITICAL] Гонка данных VT-эмулятора.** Фоновая горутина `ptmx.Read` писала в `w.emu.Write`, пока главный поток `Draw()` звал `emu.Resize`/`emu.Draw`. Сырой `vt.Emulator` не синхронизирован. Фикс: `w.emu` теперь `*vt.SafeEmulator` (Write/Resize/Draw/Cursor* — библиотечный мьютекс) + ВСЕ обращения к состоянию эмулятора сериализованы через `w.mu` (Draw весь под локом). Отдельно: `vt.SafeEmulator` НЕ защищает `Read()`/`Close()` — гонку на внутреннем `closed`-флаге устранили, отказавшись от `emu.Close()`: разблокируем goroutine-копировальщик закрытием входного пайпа эмулятора (`InputPipe().(*io.PipeWriter).CloseWithError(io.EOF)`).
- **Ч2 [PERF] Троттлинг фоновых pty.** Раньше каждый байт от любого окна звал `app.QueueUpdateDraw`. Теперь `notifyOutput()` жжёт красный только если окно АКТИВНОЕ (`isActive func() bool` от WM → `wm.isActiveWindow`); фоновые эмуляторы тихо копят вывод в VT-буфер (переключение и так рисует tview заново).
- **Ч3 [UX/ARCH] Перехват F-клавиш WM.** Раньше `SetInputCapture` глотал F2–F8 всегда → mc/htop/vim теряли F-клавиши. Фикс (основа под префиксные хоткеи): **пока фокус в shell, обычные F-клавиши уходят внутрь, команды WM — Alt+Fn**; из базы/виджета обычные F работают как раньше. `keyBytes` дополнен переводами F1–F12 (`\x1bOP`..`\x1b[24~`) — иначе passthrough был бы мёртвым.
- Проверки: `go build`, `go vet`, `go test -race -count=1 ./...` — ok (6 тестов: +4 новых, включая `TestShellWindowResizeWriteRace` с реальным bash под `yes`-флудом и draw/resize в цикле; найден и исправлен расивший pre-existing `TestPTYEndToEnd`). pty-смок `/tmp/opencode/smoke45.py` (1 прогон 15/15 + 2 повтора 15/15): не-Активное окно не закрывается обычным F4; F4 реально доходит до shell (cat-capture `\x1bOS`); Alt+F2/F3/F4/F7 работают при фокусе shell; активное окно отзывчиво при BKG_FLOOD в неактивном (это и есть критерий троттлинга); save/restore через Alt+F7/F8; Ctrl-C выход; следов флуда и зомби нет.
- Следующий: Этап 5 (обзор с живыми миниатюрами pty) на 2026-10-16 / по факту использования.

## 2026-10-15 16:48 — [DECISION] Старт проекта, выбран путь SPIKE с charmbracelet/x/vt
- Что: Оценена сложность и полезность идеи wm-tview; изучен Txmux (`/root/.local/bin/Txmux/`). Пользователь подтвердил диагноз боли: «хочу всё внутри своей TUI» (вариант 2), поэтому wm-tview обязан владеть рендерингом и без VT-эмуляции не обойтись.
- Где: AGENTS.md (SEED, roadmap), HISTORY.md.
- Решения:
  - Референс `tview/demos/embed` из SEED в текущем tview **отсутствует** (проверено по GitHub API и модуль-кэшу). Вики tview даёт только `ANSIWriter`/`TranslateANSI` (SGR-цвета, без курсора/перерисовки) — этого мало для живого shell.
  - VT-эмулятор: `github.com/charmbracelet/x/vt` v0.1.0 (Oct 2026) вместо мёртвого `vt10x` (2022). Есть alt-screen, truecolor, mouse, scrollback, cursor. Библиотечный `Emulator.Draw(uv.Screen, area)`; нужен тонкий адаптер `uv.Screen`→`tcell.Screen`.
  - Из Txmux берём только модель «слот = `cmd|path|name`» + save/restore. Выбрасываем делегирование GUI-эмулятору, угадывание состояния по `/proc`, `eval`-склейку команд. Txmux не трогаем (вне проекта).
- Проверки: `go version` = go1.24.4; доступ к proxy.golang.org есть; версии крейт-пакетов резолвятся.
- Следующий: реализовать каркас spike в `prototype/` (go.mod, main.go, shell_window.go).

## 2026-10-15 17:05 — [FEAT] Этап 0 (SPIKE) завершён, вердикт go
- Что: Собран и проверен фильтрующий прототип: tview-фон + модальное окно с живым bash (creack/pty) поверх основного UI, рендер через charmbracelet/x/vt.
- Где: `prototype/` (main.go, shell_window.go, spike_test.go).
- Ключевые находки:
  - `tview/demos/embed` из SEED отсутствует; `ANSIWriter` не даёт курсора/перерисовки → нужен VT-эмулятор.
  - Выбран `charmbracelet/x/vt` v0.1.0; интеграция требует адаптера `uv.Screen`→`tcell.Screen` (~60 строк в shell_window.go).
  - Модальное окно поверх UI — кастомный `overlay`-примитив (Draw base затем top 80%), не Pages.
  - Важный фикс: tview шлёт клавиши в root только при `root.HasFocus()`; overlay обязан возвращать true, пока поверх есть окно (main.go HasFocus).
- Проверки: `go build ./...`, `go vet ./...`, `go test ./...` (адаптер-тест + pty end-to-end с настоящим bash) — все ok. Интерактивный smoke через pty-харнесс (реальный бинарь, без стабов): 8/8 чеков PASS — F1 открытие, bash-промпт, вывод команды, `ls --color`, `less` (alt-screen) и возврат промпта, Ctrl-D закрытие, F1 reopen, Ctrl-C выход; зомби нет; `vtest audit` чисто (стабы не использовались — в смок-тесте только реальные бинари).
- Риски SEED: R-1 (overlay) решён кастомным примитивом; R-2 (мерцание) не наблюдался — единый цикл tview + QueueUpdateDraw; R-3 (курсор) решён переносом эмуляторного курсора в `screen.ShowCursor` со сдвигом окна; R-4 (alt-screen) работает (less); R-5 (TERM) решён `TERM=xterm-256color` для дочернего bash.
- Следующий: Этап 1 — Шаг 1 SEED (одно окно, критерии A1–A10) на этом эмуляторе.

## 2026-10-15 17:20 — [FEAT] Этап 1 (Шаг 1 SEED) завершён: одно окно, A1–A10
- Что: Прототип доведён до критериев SEED Шага 1. Хоткей открытия/закрытия окна изменён с F1 на **F2** (F1 в терминале — «справка»), базовый текст обновлён.
- Где: `prototype/main.go`, `prototype/shell_window.go`.
- Найденный и исправленный баг (Ctrl-C): tview вызывает `app.Stop()`, если `inputCapture` вернул тот же самый event для Ctrl-C. Из-за этого Ctrl-C при открытом окне убивал приложение вместо отправки в shell. Фикс: при открытом окне возвращать копию события (`e := *event; return &e`), см. `prototype/main.go`. Это также восстановило работоспособность ресайза и закрытия после прерывания foreground-задачи.
- Проверки: `go build`, `go vet`, `go test` — ok. Интерактивный pty-смок (реальные бинари, без стабов): **12/12 PASS** — A3 base UI; A4 F2 открывает окно + живой промпт; A5 вывод команды, история `↑`, Tab-дополнение (файл в `/tmp/opencode`), Ctrl-C прерывает `sleep 30`, `ls --color`; A8 ресайз сохраняет окно+промпт; A6 Ctrl-D закрывает; A7 F2 открывает заново; Ctrl-C выходит. A9 — зомби bash нет; A10 — `less` (alt-screen) рендерится и после `q` возвращает промпт; `vtest audit` чисто.
- Следующий: Этап 2 — окна-виджеты + N окон, фокус и переключение.

## 2026-10-15 17:35 — [FEAT] Этап 2 завершён: окна-виджеты + N окон, фокус/переключение
- Что: Введён `WM` — минимальный менеджер окон поверх tview. `overlay` (главное окно + один top) заменён на список `[]*Window` с активным окном. `Window.content` — ЛИБО `*shellWindow` (pty), ЛИБО произвольный `tview.Primitive` (виджет). Рисуется только активное окно; неактивные окна буферизуются (их VT-эмуляторы продолжают принимать вывод pty). Хоткеи: F2 новое shell-окно, F3 следующее окно, F4 закрыть активное, F6 демо-окно-виджет.
- Где: новый `prototype/wm.go`; переписан `prototype/main.go`; `prototype/shell_window.go` без изменений.
- Проверки: `go build`, `go vet`, `go test` — ok. Интерактивный pty-смок (реальные бинари, без стабов): **20/20 PASS** — base UI; два shell-окна; ввод маркируется по активному окну (SHELL_A/SHELL_B); F3-цикл меняет активное окно (индекс в заголовке `[i/n]`); окно-виджет (tview.TextView) сосуществует с shell; буфер неактивного окна сохраняется после переключения туда-обратно; F4 закрывает по одному до base UI; Ctrl-D закрывает shell через watcher-путь (`removeByShell`); Ctrl-C выход. Зомби bash нет; `vtest audit` чисто.
- Принятые риски: R2-1 (F-клавиши WM глобальны, не доходят до программ в окне — решим префиксом позже); R2-2 (неактивные pty триггерят лишний Draw — оптимизация позже).
- Следующий: Этап 3 — Context save/restore (идея Txmux: слоты `cmd|path|name`).

## 2026-10-15 17:50 — [FEAT] Этап 3 завершён: context save/restore (слоты Txmux)
- Что: Добавлено сохранение/восстановление рабочего окружения. Формат слота `kind|cmd|path|name` (pipe-delimited, POSIX-friendly): shell-окно записывает живой cwd процесса (через `/proc/<pid>/cwd`, как в Txmux), widget — только имя. Файл по умолчанию `~/.config/wm-tview/session.conf`, переопределяется `WM_SESSION_FILE`. Хоткеи: F7 — сохранить, F8 — восстановить. `restore()` закрывает все окна и открывает заново (shell в сохранённой директории через `exec.Cmd.Dir`, widget — демо). Статус-строка в базовом UI.
- Где: новый `prototype/session.go`; `prototype/wm.go` (`snapshot`/`restore`/`openShellAt`/`closeAll`); `prototype/shell_window.go` (`newShellWindow(dir, ...)`, метод `Cwd()`); `prototype/main.go` (F7/F8, статус).
- Проверки: `go build`, `go vet`, `go test` — ok. Интерактивный pty-смок (реальные бинари, без стабов, `WM_SESSION_FILE` в /tmp): **20/20 PASS** — 2 shell (`cd /tmp/opencode`, `cd /root`) + widget; F7 → файл из 3 корректных слотов; F4 закрыть всё → base со статусом `saved: 3`; F8 → окна [1/3..3/3], widget восстановлен, cwd каждого shell проверен `pwd` (`/tmp/opencode`, `/root`), ввод работает; F4 закрыть всё → статус `restored: 3`; F7 при 0 окнах → пустой файл; выход. Зомби нет; `vtest audit` чисто.
- Нюанс: bash отображает `/root` как `~` (== `$HOME`), поэтому cwd в тесте проверялся через `pwd`.
- Следующий: Этап 4 — интеграция с me.

## 2026-10-15 18:10 — [FEAT] Этап 4 завершён: интеграция с me (`me wm`)
- Что: Метод `me wm` — запускалка TUI-менеджера окон. `~/.local/bin/me/me_lib/wm.sh` (exec `~/.local/bin/wm-tview`); регистрация в **активном** реестре `/root/.config/me/me.conf` (`#@method: wm` + `me_method_wm()`). Бинарь собран из `prototype/` в `~/.local/bin/wm-tview`. В `main.go` добавлен авто-restore сохранённой сессии при старте (пустое базовое окно, если сессии нет).
- Где: `~/.local/bin/me/me_lib/wm.sh` (новый), `/root/.config/me/me.conf` (добавлен блок метода), `prototype/main.go` (авто-restore).
- Решения:
  - Репо-копия `~/.local/bin/me/me.conf` не тронута (активный конфиг — `~/.config/me/me.conf`, как указал пользователь; иначе `install_me.sh` перезапишет активный).
  - Кэш `me` (`~/.cache/me/*`) инвалидируется автоматически по md5 me.conf — отдельный сброс не нужен.
  - IPC (открывать окна в уже запущенном экземпляре) не делалось — этап «запускалка».
- Проверки: `go build/vet/test` — ok; `bash -n` для `wm.sh` и `me.conf` — ok; интеграционный pty-смок (реальные `me` + бинарь + bash, без стабов): **8/8 PASS** — с сессией (shell в /tmp/opencode + widget) авто-restore при старте, widget активен, F3→shell, `pwd` = /tmp/opencode; без сессии — пустой workspace; зомби нет; `vtest audit` чисто.
- Следующий: этапы 0–4 закрыты. Дальнейшее — по факту использования `me wm`: Этап 5 (метод как окно / меню методов в TUI) или эксплуатация. По правилу SEED: «если через месяц не используется ежедневно — остановиться».

## 2026-10-15 18:20 — [DECISION] Запланирован Этап 5: обзор с живыми миниатюрами pty (на 2026-10-16)
- Что: Пользователь подтвердил функцию «окно-виджет с миниатюрами всех открытых pty». Записано требование: миниатюры переключаемы; Enter на выделенной — вход в выбранный pty (окно активируется). Код сегодня НЕ пишется.
- Где: roadmap в AGENTS.md (Этап 5), проект /root/DEV/tess/.
- Техническая база (из разговора): у каждого shell-окна есть VT-буфер (`emu.CellAt`), миниатюра = nearest-выборка по сетке; обзор — отдельный `Window` (`kind=overview`, `overview.go`), в сессию не пишется; риски R-OV1..R-OV6 зафиксированы.
- Проверки: нет (план только).
- Следующий: 2026-10-16 — выбрать режим (A: обзор-окно вместо F6 / B: оверлей-переключатель), реализовать, пройти pty-смок.

## 2026-10-17 10:25 — [FIX] Блочная вставка из буфера вместо побуквенной
- Что: Вставка кода из буфера (нативный Ctrl+Shift+V / правая кнопка) шла ПОБУКВЕННО в pty. Причина: в main.go не было app.EnablePaste(true) — tview при enablePaste=false игнорировал EventPaste, и текст приходил как поток отдельных EventKey в InputHandler. У tview-дефолта Box.PasteHandler (WrapPasteHandler(nil)) текст вообще проглатывается — поэтому оба пункта правились вместе.
- Где: prototype/main.go (EnablePaste(true)); prototype/shell_window.go (PasteHandler(); writeTextToPty переведён с сырого io.WriteString на vt.SafeEmulator.Paste; pasteClipboard упрощён до вызова writeTextToPty).
- Дополнительно: emu.Paste сам оборачивает текст в bracketed paste (\x1b[200~…\x1b[201~), если программа внутри включила ?2004 — закрывает и Shift+Insert/среднюю кнопку (раньше писали без обёртки, из-за чего vim/readline могли триггерить autoindent/автодополнение).
- Проверки: go build/vet ok; go test -race -count=1 ./... — все тесты зелёные, +новый TestShellWindowPasteHandlerBlock (многострочный блок CRLF/LF через PasteHandler выполняется в bash целиком). Бинарь пересобран в ~/.local/bin/wm-tview.
- Следующий: проверить вживую — вставить 20-строчный скрипт в окно shell, убедиться что выполняется блочно.


## 2026-10-20 22:45 — [FEAT] ЛКМ-выделение в pty-окне + заголовок tess_wm
- Что: Пользователь: «нет выделения в vt левой кнопкой мыши; в заголовке сейчас wm-tview хочу tess_wm». Реализовано терминальное выделение: ЛКМ-драг отмечает прямоугольник (включая scrollback, координаты виртуального потока), при отпускании прямоугольник нормализуется (normalizeSel), сохраняется как selStart/selEnd и остаётся подсвеченным (mouseSelVisible) до следующего клика; текст копируется в CLIPBOARD+PRIMARY через setClipboard. Обычный клик без драга сбрасывает выделение (как в реальном терминале). Заголовок базового окна: «wm-tview (…)» → «tess_wm (…)».
- Где: prototype/shell_window.go — новый MouseHandler с tview mouse-capture (MouseLeftDown возвращает w, MouseMove/LeftUp долетают даже за пределами rect); streamPosFromScreen() (screen→stream с учётом scrollOff, Y<0 = история); finishMouseSel(); paintSelRect() выделен в общий хелпер (используется и copy-оверлеем, и мышью); Draw() рисует активный драг/закоммиченное выделение. prototype/main.go — заголовок + строка справки про ЛКМ.
- Проверки: go build/vet ok; go test -race -count=1 ./... — все зелёные, +TestMouseDragSelectsText (drag-захват, перемещение, коммит в selStart/selEnd, клик-очистка). Бинарь пересобран в ~/.local/bin/wm-tview.
- Следующий: справка базы обновлена (ЛКМ-драг), при желании дублировать блок в AGENTS.md справку.
