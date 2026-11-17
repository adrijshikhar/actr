Output "docs/images/actr-monitor.gif"

Set Shell "zsh"
Set FontSize 14
Set FontFamily "Menlo, JetBrains Mono, monospace"
Set Width 1200
Set Height 680
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set Padding 18

Type "./bin/actr monitor --demo"
Enter
Sleep 1.5s

Down
Sleep 400ms
Down
Sleep 400ms
Down
Sleep 1s

Screenshot "docs/images/actr-monitor.png"
Sleep 2s

Type "z"
Sleep 1.5s
Screenshot "docs/images/actr-monitor-zoomed.png"
Sleep 2s

Type "z"
Sleep 1.5s

Type "q"
Sleep 500ms
