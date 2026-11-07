package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/murphytalk/urphin/internal/theme"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMainSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Main Suite")
}

var _ = Describe("theme import command", func() {
	It("writes a validated Base16 theme without overwriting an existing file", func() {
		dir := GinkgoT().TempDir()
		source := filepath.Join(dir, "sample.yaml")
		input := "scheme: sample\n" +
			"base00: '000000'\nbase01: '111111'\nbase02: '222222'\nbase03: '333333'\n" +
			"base04: '444444'\nbase05: '555555'\nbase06: '666666'\nbase07: '777777'\n" +
			"base08: '888888'\nbase09: '999999'\nbase0A: 'aaaaaa'\nbase0B: 'bbbbbb'\n" +
			"base0C: 'cccccc'\nbase0D: 'dddddd'\nbase0E: 'eeeeee'\nbase0F: 'ffffff'\n"
		Expect(os.WriteFile(source, []byte(input), 0o600)).To(Succeed())
		dest := filepath.Join(dir, "themes")
		Expect(importBase16Theme(source, dest)).To(Succeed())
		loaded := theme.Load("sample", filepath.Join(dir, "config.yaml"))
		Expect(loaded.Colors[theme.FG]).To(Equal(theme.Color("#555555")))
		Expect(importBase16Theme(source, dest)).To(HaveOccurred())
	})

	It("rejects unsafe filenames before creating the destination", func() {
		dir := GinkgoT().TempDir()
		source := filepath.Join(dir, "bad name.yaml")
		Expect(os.WriteFile(source, []byte("irrelevant"), 0o600)).To(Succeed())
		Expect(importBase16Theme(source, filepath.Join(dir, "themes"))).To(HaveOccurred())
		_, err := os.Stat(filepath.Join(dir, "themes"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})

var _ = Describe("theme selector precedence", func() {
	It("lets a provided flag override the config, including an empty default selection", func() {
		Expect(resolveThemeSelector("custom", "ansi", true)).To(Equal("ansi"))
		Expect(resolveThemeSelector("custom", "", true)).To(BeEmpty())
		Expect(resolveThemeSelector("custom", "", false)).To(Equal("custom"))
	})
})
