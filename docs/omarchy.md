# Chasen on Omarchy

On [Omarchy](https://omarchy.org), Chasen fits in three places: the menu of apps, the top bar, and the colors of your theme. Each one is one command.

## In the menu of apps

```bash
omarchy-tui-install Chasen chasen tile https://chasenhq.com/icon.png
```

Chasen is now in the app launcher (`Super + Space`). It opens the screen of your servers in a terminal of its own.

## In the top bar

The plugin [omarchy-chasen](https://github.com/karloscodes/omarchy-chasen) puts a server icon in the bar of Omarchy. It is dim when all is well, normal when a server has a warning, and in the urgent color when something is wrong. Its tooltip lists the alerts of all your servers, and a click opens the screen.

```bash
omarchy plugin add https://github.com/karloscodes/omarchy-chasen.git --enable
```

It runs `chasen alerts --waybar` once a minute. That asks every server that you are logged in to: a moment of SSH, and nothing runs on the server between two asks. [The reference](reference.md#alerts) lists the alerts. To remove it: `omarchy plugin remove karloscodes.chasen`.

**With an Omarchy that has Waybar** (before its own shell), add a module instead. Put `"custom/chasen"` in `modules-right` of `~/.config/waybar/config.jsonc`, add this next to the other modules, and run `omarchy-restart-waybar`:

```jsonc
"custom/chasen": {
  "exec": "chasen alerts --waybar",
  "return-type": "json",
  "interval": 60,
  "on-click": "omarchy-launch-tui chasen"
}
```

## In the colors of your theme

Press `t` in the screen until it says `terminal`. The screen then uses the colors of your terminal, and Omarchy sets those colors for each theme, so the screen changes with every theme you switch to. The screen remembers the choice.
