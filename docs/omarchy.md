# Chasen on Omarchy

On [Omarchy](https://omarchy.org), Chasen fits in three places: the menu of apps, the top bar, and the colors of your theme. Each one is a few lines, and none needs anything from Omarchy that it does not have.

## In the menu of apps

```bash
omarchy-tui-install Chasen chasen tile https://chasenhq.com/icon.png
```

Chasen is now in the app launcher (`Super + Space`). It opens the screen of your servers in a terminal of its own.

## In the top bar

The bar shows a dot and the number of alerts of all your servers, and nothing when there are none. The tooltip lists them, server by server. A click opens the screen.

Add the module to `~/.config/waybar/config.jsonc`. Put `"custom/chasen"` in `modules-right`, then add this next to the other modules:

```jsonc
"custom/chasen": {
  "exec": "chasen alerts --waybar",
  "return-type": "json",
  "interval": 60,
  "on-click": "omarchy-launch-tui chasen"
}
```

Give it a color in `~/.config/waybar/style.css`:

```css
#custom-chasen { margin: 0 7.5px; }
#custom-chasen.warning { color: #e0af68; }
#custom-chasen.error { color: #f7768e; }
```

Then run `omarchy-restart-waybar`.

`chasen alerts --waybar` asks every server that you are logged in to, once a minute. Each server answers from its own files: it costs a moment of SSH, and nothing runs on the server between two asks. [The reference](reference.md#alerts) lists the alerts.

## In the colors of your theme

The screen has the theme of Omarchy built in: press `t` in it until it says `omarchy`. It takes the accent and the red of the current theme, and changes with it when you switch themes. The screen remembers the choice.
