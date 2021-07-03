# twitchstream

twitchstream is a web server that can stream video from twitch without ads, so you can use a video player
instead of the resource intensive website.

There are also some scripts to better integrate twitchstream with browser.

## Quickstart

### Run the server
```
go get github.com/chengzhicn/twitchstream
twitchstream
```
By default the server will listening on port 7313. You can also using the service file `twitchstream.init`
to autostart twitchstream.

### Open stream
```
mpv http://localhost:7313/e3
```
This will open mpv to stream E3, you can replace `e3` with the streamer you like.

### Integrate with browser
Register mpv schema in browser.
```
cp mpvp.desktop ~/.local/share/applications/
cp mpv.sh /usr/local/bin/
```

Import greasemonkey script `twitch_redirect.user.js` to automatic redirect the streamer page you opened
in browser to mpv.
