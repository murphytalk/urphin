package main

import (
	"flag"
	"io"
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

func executeRun(args ...string) (int, string, func()) {
	oldFlags, oldStdout := flag.CommandLine, os.Stdout
	oldCfgPath, oldThemeOverride := cfgPath, themeOverride
	oldListThemes, oldImportTheme, oldCmdLine := listThemes, importTheme, cmdLine
	flags := flag.NewFlagSet("theme-test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfgPath, "config", "", "config")
	flags.StringVar(&themeOverride, "theme", "", "theme")
	flags.BoolVar(&listThemes, "list-themes", false, "list")
	flags.StringVar(&importTheme, "import-theme", "", "import")
	flags.StringVar(&cmdLine, "C", "", "command")
	if err := flags.Parse(args); err != nil {
		panic(err)
	}
	flag.CommandLine = flags
	reader, writer, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = writer
	code := run()
	_ = writer.Close()
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		panic(err)
	}
	cleanup := func() {
		flag.CommandLine, os.Stdout = oldFlags, oldStdout
		cfgPath, themeOverride = oldCfgPath, oldThemeOverride
		listThemes, importTheme, cmdLine = oldListThemes, oldImportTheme, oldCmdLine
	}
	return code, string(output), cleanup
}

func base16TestYAML() string {
	return "scheme: sample\n" +
		"base00: '000000'\nbase01: '111111'\nbase02: '222222'\nbase03: '333333'\n" +
		"base04: '444444'\nbase05: '555555'\nbase06: '666666'\nbase07: '777777'\n" +
		"base08: '888888'\nbase09: '999999'\nbase0A: 'aaaaaa'\nbase0B: 'bbbbbb'\n" +
		"base0C: 'cccccc'\nbase0D: 'dddddd'\nbase0E: 'eeeeee'\nbase0F: 'ffffff'\n"
}

type failedThemeFile struct {
	writeErr, closeErr error
	closed             bool
}

func (f *failedThemeFile) Write([]byte) (int, error) { return 0, f.writeErr }
func (f *failedThemeFile) Close() error {
	f.closed = true
	return f.closeErr
}

var _ = Describe("theme import command", func() {
	It("writes a validated Base16 theme without overwriting an existing file", func() {
		dir := GinkgoT().TempDir()
		source := filepath.Join(dir, "sample.yaml")
		Expect(os.WriteFile(source, []byte(base16TestYAML()), 0o600)).To(Succeed())
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

var _ = Describe("theme utility flags", func() {
	It("lists embedded themes without opening config or backend", func() {
		configPath := filepath.Join(GinkgoT().TempDir(), "missing.yaml")
		code, output, cleanup := executeRun("-config", configPath, "-list-themes")
		defer cleanup()
		Expect(code).To(Equal(0))
		Expect(output).To(Equal("ansi\ncatppuccin-mocha\n"))
	})

	It("rejects conflicting utility flags and utility flags with -C", func() {
		dir := GinkgoT().TempDir()
		code, _, cleanup := executeRun("-config", filepath.Join(dir, "missing.yaml"), "-list-themes", "-import-theme", filepath.Join(dir, "x.yaml"))
		cleanup()
		Expect(code).To(Equal(2))
		code, _, cleanup = executeRun("-config", filepath.Join(dir, "missing.yaml"), "-list-themes", "-C", "help")
		cleanup()
		Expect(code).To(Equal(2))
		code, _, cleanup = executeRun("-config", filepath.Join(dir, "missing.yaml"), "-import-theme=")
		cleanup()
		Expect(code).To(Equal(2))
	})

	It("imports a Base16 file with no config or backend startup", func() {
		dir := GinkgoT().TempDir()
		source := filepath.Join(dir, "sample.yaml")
		input := "scheme: sample\n" +
			"base00: '000000'\nbase01: '111111'\nbase02: '222222'\nbase03: '333333'\n" +
			"base04: '444444'\nbase05: '555555'\nbase06: '666666'\nbase07: '777777'\n" +
			"base08: '888888'\nbase09: '999999'\nbase0A: 'aaaaaa'\nbase0B: 'bbbbbb'\n" +
			"base0C: 'cccccc'\nbase0D: 'dddddd'\nbase0E: 'eeeeee'\nbase0F: 'ffffff'\n"
		Expect(os.WriteFile(source, []byte(input), 0o600)).To(Succeed())
		code, output, cleanup := executeRun("-config", filepath.Join(dir, "missing.yaml"), "-import-theme", source)
		defer cleanup()
		Expect(code).To(Equal(0))
		Expect(output).To(Equal(filepath.Join(dir, "themes", "sample.yaml") + "\n"))
		info, err := os.Stat(filepath.Join(dir, "themes", "sample.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		info, err = os.Stat(filepath.Join(dir, "themes"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("does not overwrite a symlink at the destination", func() {
		dir := GinkgoT().TempDir()
		source := filepath.Join(dir, "sample.yaml")
		Expect(os.WriteFile(source, []byte(base16TestYAML()), 0o600)).To(Succeed())
		themesDir := filepath.Join(dir, "themes")
		Expect(os.Mkdir(themesDir, 0o700)).To(Succeed())
		target := filepath.Join(dir, "target")
		Expect(os.WriteFile(target, []byte("keep"), 0o600)).To(Succeed())
		Expect(os.Symlink(target, filepath.Join(themesDir, "sample.yaml"))).To(Succeed())
		Expect(importBase16Theme(source, themesDir)).To(HaveOccurred())
		contents, err := os.ReadFile(target)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(contents)).To(Equal("keep"))
	})

	It("removes incomplete output after write or close errors", func() {
		for _, test := range []struct {
			name               string
			writeErr, closeErr error
		}{
			{"write", os.ErrInvalid, nil},
			{"close", nil, os.ErrInvalid},
		} {
			path := filepath.Join(GinkgoT().TempDir(), test.name+".yaml")
			Expect(os.WriteFile(path, []byte("incomplete"), 0o600)).To(Succeed())
			file := &failedThemeFile{writeErr: test.writeErr, closeErr: test.closeErr}
			Expect(writeThemeFile(path, file, []byte("theme"))).To(MatchError(os.ErrInvalid))
			Expect(file.closed).To(BeTrue())
			_, err := os.Stat(path)
			Expect(os.IsNotExist(err)).To(BeTrue())
		}
	})
})
