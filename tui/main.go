package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

var (
	cfgPath string
	logPath string
	cliCmd  = &cobra.Command{
		Use:   "urphin",
		Short: "urphin is a handcrafted personal finance dashboard",
		Args:  cliArgsCheck,
		Run:   run,
	}
)

func cliArgsCheck(cmd *cobra.Command, args []string) error {
	return nil
}

func run(cmd *cobra.Command, args []string) {
	fmt.Println("args are", args)
}

func main() {
	if err := cliCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("Missing home directory")
		return
	}

	cliCmd.Flags().StringVarP(&cfgPath, "config", "c", filepath.Join(home, "urphin", "urphin.yaml"), "config file")
	cliCmd.Flags().StringVarP(&logPath, "log", "l", filepath.Join(home, "urphin", "urphin.log"), "log file")
}
