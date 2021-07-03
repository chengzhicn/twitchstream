// ==UserScript==
// @name        twitch redirect
// @namespace   twitch@redirect
// @include     /^https://www.twitch.tv/(?!directory)[^/]+$/
// @version     1
// @grant       none
// @noframes
// @run-at      document-start
// ==/UserScript==
window.location = window.location.href.replace("https://www.twitch.tv/", "mpv:http://localhost:7313/")
