Output "docs/images/actr-monitor.gif"

Set Shell "zsh"
Set FontSize 14
Set FontFamily "Menlo, JetBrains Mono, monospace"
Set Width 1300
Set Height 820
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set Padding 18

Type "./bin/actr monitor --demo"
Enter
Sleep 1.5s

# Navigate down to Compile binaries in cxstatusline / release-prebuilt
Down
Sleep 300ms
Down
Sleep 300ms

# Expand telemetry HUD to reveal 4 real-time Braille wave graphs and host vitals
Type "e"
Sleep 2s

# Capture main hero screenshot: expanded live monitor with graphs, logs, and host vitals
Screenshot "docs/images/actr-monitor.png"
Sleep 2.5s

# Toggle fullscreen log zoom (z)
Type "z"
Sleep 1.5s
Screenshot "docs/images/actr-monitor-zoomed.png"
Sleep 2s

# Exit fullscreen log zoom (z)
Type "z"
Sleep 1s

# Navigate down into Historical Runs -> actr -> release-v0.1.0
Down
Sleep 150ms
Down
Sleep 150ms
Down
Sleep 150ms
Down
Sleep 150ms
Down
Sleep 150ms
Enter
Sleep 250ms
Down
Sleep 150ms
Enter
Sleep 250ms
Down
Sleep 150ms
Down
Sleep 150ms
Sleep 1.5s

# Capture historical run inspection with past execution profiles
Screenshot "docs/images/actr-monitor-historical.png"
Sleep 2.5s

# Gracefully detach monitor
Type "q"
Sleep 500ms
