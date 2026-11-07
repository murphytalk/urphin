package util_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/murphytalk/urphin/internal/theme"
	. "github.com/murphytalk/urphin/internal/util"
)

var _ = Describe("Util", func() {

	stylesFixture := theme.Styles{
		Text:      func(v string) string { return v },
		TextLight: func(v string) string { return v },
		TextLabel: func(v string) string { return v },
		TextBold:  func(v string) string { return v },
		TextLine:  func(v string) string { return v },
		TextPrice: func(percent float64, text string) string { return text },
		Tag:       func(v string) string { return v },
	}

	Describe("ConvertFloatToString", func() {
		It("should convert a float to a precision of two", func() {
			output := ConvertFloatToString(0.563412, false)
			Expect(output).To(Equal("0.56"))
		})

		When("there using variable precision", func() {
			It("should convert a float that smaller than 10 to a string with a precision of four", func() {
				output := ConvertFloatToString(0.563412, true)
				Expect(output).To(Equal("0.5634"))
			})
			It("should convert a float that between 10 and 100 to a string with a precision of three", func() {
				output := ConvertFloatToString(12.5634, true)
				Expect(output).To(Equal("12.563"))
			})
			It("should convert a float that greater than 100 to a string with a precision of two", func() {
				output := ConvertFloatToString(204.4325, true)
				Expect(output).To(Equal("204.43"))
			})
			It("should set a precision of two when the value is zero", func() {
				output := ConvertFloatToString(0.0, true)
				Expect(output).To(Equal("0.00"))
			})
			It("should set a precision of one when the value is negative and over 1000", func() {
				output := ConvertFloatToString(-2000.0, true)
				Expect(output).To(Equal("-2000.0"))
			})
			It("should set a precision of zero when the value is over 10000", func() {
				output := ConvertFloatToString(10000.0, true)
				Expect(output).To(Equal("10000.00"))
			})
			It("should append a M when the value is over a million", func() {
				output := ConvertFloatToString(43523398, true)
				Expect(output).To(Equal("43.523 M"))
			})
			It("should append a M when the value is over a billion", func() {
				output := ConvertFloatToString(43523398000, true)
				Expect(output).To(Equal("43.523 B"))
			})
			It("should append a M when the value is over a trillion", func() {
				output := ConvertFloatToString(43523398000000, true)
				Expect(output).To(Equal("43.523 T"))
			})
		})

	})
	Describe("ValueText", func() {
		When("value is <= 0.0", func() {
			It("should return an empty string", func() {
				output := ValueText(0.0, stylesFixture)
				Expect(output).To(ContainSubstring(""))
			})
		})
		It("should generate text for values", func() {
			output := ValueText(435.32, stylesFixture)
			expectedOutput := "435.32"
			Expect(output).To(Equal(expectedOutput))
		})
	})
	It("formats known costs with the shared numeric formatter", func() {
		for _, test := range []struct {
			value    float64
			variable bool
			want     string
		}{{0, false, "0.00"}, {0, true, "0.00"}, {-10, false, "-10.00"}, {-10, true, "-10.000"}, {-1000, false, "-1000.00"}, {-1000, true, "-1000.0"}, {0.0001, true, "0.0001"}, {-0.0001, true, "-0.0001"}, {123456789, false, "123456789.00"}} {
			Expect(ConvertFloatToString(test.value, test.variable)).To(Equal(test.want))
		}
	})
	It("adds thousands separators without changing precision or units", func() {
		for _, test := range []struct {
			value    float64
			variable bool
			want     string
		}{
			{1234.56, false, "1,234.56"},
			{-1234567.89, false, "-1,234,567.89"},
			{12345.6789, true, "12,345.68"},
			{43523398, true, "43.523 M"},
			{0.0001, true, "0.0001"},
		} {
			Expect(ConvertFloatToStringWithThousandsSeparator(test.value, test.variable)).To(Equal(test.want))
		}
	})
})
