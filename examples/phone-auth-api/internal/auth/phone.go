package auth

import (
	"strings"
	"unicode"
)

type IranPhoneNormalizer struct{}

var iranMobilePrefixes = []string{
	"900", "901", "902", "903", "904", "905", "91", "920", "921", "922", "923", "93",
	"990", "991", "992", "993", "994", "99510", "99550", "996", "9981", "9982",
	"99830", "99831", "99832", "99888", "99900", "99901", "99902", "99903", "9991",
	"99921", "99930", "99931", "99932", "99933", "99934", "9995", "99969", "99977",
	"9998", "9999",
}

func (IranPhoneNormalizer) Normalize(input string) (string, error) {
	if input == "" || len(input) > 256 {
		return "", ErrInvalidPhone
	}
	var cleaned strings.Builder
	cleaned.Grow(len(input))
	for _, r := range input {
		switch {
		case r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		case r >= '۰' && r <= '۹':
			cleaned.WriteByte(byte('0' + r - '۰'))
		case r >= '٠' && r <= '٩':
			cleaned.WriteByte(byte('0' + r - '٠'))
		case r == '+' || r == '(' || r == ')' || isPhoneHyphen(r) || unicode.IsSpace(r):
			if r == '+' {
				cleaned.WriteRune(r)
			}
		default:
			return "", ErrInvalidPhone
		}
	}
	value := cleaned.String()
	var national string
	switch {
	case strings.HasPrefix(value, "+98"):
		national = value[3:]
	case strings.HasPrefix(value, "0098"):
		national = value[4:]
	case strings.HasPrefix(value, "0"):
		national = value[1:]
	default:
		national = value
	}
	if len(national) != 10 || strings.Contains(national, "+") || !hasIranMobilePrefix(national) {
		return "", ErrInvalidPhone
	}
	for i := range len(national) {
		if national[i] < '0' || national[i] > '9' {
			return "", ErrInvalidPhone
		}
	}
	return "+98" + national, nil
}

func isPhoneHyphen(r rune) bool {
	return r == '-' || r == '\u2010' || r == '\u2011' || r == '\u2012' ||
		r == '\u2013' || r == '\u2014' || r == '\u2212'
}

func hasIranMobilePrefix(national string) bool {
	for _, prefix := range iranMobilePrefixes {
		if strings.HasPrefix(national, prefix) {
			return true
		}
	}
	return false
}

func normalizeCode(input string) (string, error) {
	if len(input) < 6 || len(input) > 12 {
		return "", ErrInvalidCode
	}
	var code strings.Builder
	code.Grow(6)
	for _, r := range input {
		switch {
		case r >= '0' && r <= '9':
			code.WriteRune(r)
		case r >= '۰' && r <= '۹':
			code.WriteByte(byte('0' + r - '۰'))
		case r >= '٠' && r <= '٩':
			code.WriteByte(byte('0' + r - '٠'))
		default:
			return "", ErrInvalidCode
		}
	}
	if code.Len() != 6 {
		return "", ErrInvalidCode
	}
	return code.String(), nil
}
