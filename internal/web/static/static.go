// Package static embeds hubCDN's brand assets - the favicon, the "not
// found" placeholder served by the image endpoint, and the mascot
// illustration used as a decorative accent on the landing page - so the
// binary stays self-contained with no external asset directory to deploy
// alongside it.
package static

import _ "embed"

//go:embed favicon.ico
var Favicon []byte

//go:embed apple-touch-icon.png
var AppleTouchIcon []byte

//go:embed mascot.webp
var Mascot []byte

//go:embed not-found.webp
var NotFound []byte
