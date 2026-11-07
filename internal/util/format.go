package util

import (
	"math"
	"strconv"
	"strings"

	"github.com/murphytalk/urphin/internal/theme"
)

func getPrecision(f float64) int {

	v := math.Abs(f)

	if v == 0.0 {
		return 2
	}

	if v >= 1000000 {
		return 0
	}

	if v < 10 {
		return 4
	}

	if v < 100 {
		return 3
	}

	if v >= 1000 && f < 0 {
		return 1
	}

	return 2
}

// ConvertFloatToString formats a float as a string including handling large or small numbers
func ConvertFloatToString(f float64, isVariablePrecision bool) string {

	var unit string

	if !isVariablePrecision {
		return strconv.FormatFloat(f, 'f', 2, 64)
	}

	if f > 1000000000000 {
		f /= 1000000000000
		unit = " T"
	}

	if f > 1000000000 {
		f /= 1000000000
		unit = " B"
	}

	if f > 1000000 {
		f /= 1000000
		unit = " M"
	}

	prec := getPrecision(f)

	return strconv.FormatFloat(f, 'f', prec, 64) + unit
}

func ConvertFloatToStringWithThousandsSeparator(f float64, isVariablePrecision bool) string {
	text := ConvertFloatToString(f, isVariablePrecision)
	numberEnd := strings.IndexByte(text, ' ')
	if numberEnd < 0 {
		numberEnd = len(text)
	}
	integerEnd := strings.IndexByte(text[:numberEnd], '.')
	if integerEnd < 0 {
		integerEnd = numberEnd
	}

	start := 0
	if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		start = 1
	}
	digits := text[start:integerEnd]
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return text
	}

	var grouped strings.Builder
	grouped.Grow(len(text) + len(digits)/3)
	grouped.WriteString(text[:start])
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteByte(byte(digit))
	}
	grouped.WriteString(text[integerEnd:])
	return grouped.String()
}

// ValueText formats a float as a styled string
func ValueText(value float64, styles theme.Styles) string {
	if value <= 0.0 {
		return ""
	}

	return styles.Text(ConvertFloatToString(value, false))
}
