# Recording the interface

The recording needs no browser, no pseudo terminal and no clock. The interface is driven with
scripted keys on a virtual clock, and every repaint is captured as the exact bytes a terminal would
receive. That produces an asciicast, which is rendered to a GIF and a video by tools that need no
display. The result is identical on every machine, so it can be regenerated in CI.

```
make record
```

which runs, in order:

```
spanline demo --cast docs/demo.cast --cast-width 108 --cast-height 30
agg docs/demo.cast docs/demo.gif --font-family Menlo --font-size 15 --fps-cap 20 --last-frame-duration 2 --theme dracula
ffmpeg -y -i docs/demo.gif -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p" -movflags +faststart -r 20 docs/demo.mp4
```

The script the recording plays is `ui.DemoScript` in `internal/ui/cast.go`. `TestCastIsDeterministic`
guards that the same script gives the same bytes twice, and `TestDrillDownFromTheOverview` plays
the drill down keys and checks that the incident and the impact really open.

To review a screen without a terminal at all:

```
SPANLINE_FRAME_DIR=/tmp/frames go test ./internal/ui/ -run TestDumpFrames
```

That writes the reading animation, the three views, a mid animation frame and a narrow terminal
frame, and the test fails if any line overflows the terminal or if a screen loses its answer.

Tools: `agg` renders asciicasts to GIF (`brew install agg`), `ffmpeg` turns the GIF into an MP4.
