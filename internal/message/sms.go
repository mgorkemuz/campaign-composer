package message

import (
	"strings"
	"unicode/utf16"
)

// GSM 03.38 basic character set. Text using only these (and the extension
// table) is sent as GSM-7: 160 characters in one SMS, 153 per part when split.
// Anything else (e.g. Turkish ş, ğ, ı, İ) switches the whole message to UCS-2:
// 70 characters in one SMS, 67 per part.
const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

// Extension characters take two GSM-7 units each.
const gsmExtension = "^{}\\[~]|€\f"

// SMSSegments returns the encoding a text needs and how many SMS parts it is
// sent as.
func SMSSegments(text string) (encoding string, segments int) {
	units := 0
	for _, r := range text {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			units++
		case strings.ContainsRune(gsmExtension, r):
			units += 2
		default:
			return "UCS-2", parts(len(utf16.Encode([]rune(text))), 70, 67)
		}
	}
	return "GSM-7", parts(units, 160, 153)
}

func parts(units, single, multi int) int {
	if units == 0 {
		return 0
	}
	if units <= single {
		return 1
	}
	return (units + multi - 1) / multi
}
