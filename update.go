package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runCmd(dir string, name string, args ...string) error {
	fmt.Println("+", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateOne(path, label string) error {
	fmt.Printf("── update %s ──\n", label)
	if !fileExists(filepath.Join(path, "flake.nix")) {
		return fmt.Errorf("not a flake root: %s", path)
	}
	if err := runCmd(path, "nix", "flake", "update", "--flake", path); err != nil {
		return err
	}
	return commitLock(path)
}

func commitLock(flake string) error {
	hasJJ := dirExists(filepath.Join(flake, ".jj"))
	hasGit := dirExists(filepath.Join(flake, ".git"))
	if !hasJJ && !hasGit {
		fmt.Printf("  (no vcs in %s; skip lock commit)\n", flake)
		return nil
	}

	if hasJJ {
		if _, err := exec.LookPath("jj"); err == nil {
			cmd := exec.Command("jj", "diff", "--name-only")
			cmd.Dir = flake
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("jj diff: %w", err)
			}
			names := map[string]bool{}
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					names[line] = true
				}
			}
			if !names["flake.lock"] {
				fmt.Println("  (flake.lock unchanged; nothing to commit)")
				return nil
			}
			if err := runCmd(flake, "jj", "commit", "-m", "flake: update lock", "flake.lock"); err != nil {
				return err
			}
			fmt.Println("  committed flake.lock")
			return nil
		}
	}

	cmd := exec.Command("git", "status", "--porcelain", "flake.lock")
	cmd.Dir = flake
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		fmt.Println("  (flake.lock unchanged; nothing to commit)")
		return nil
	}
	if err := runCmd(flake, "git", "add", "flake.lock"); err != nil {
		return err
	}
	if err := runCmd(flake, "git", "commit", "-m", "flake: update lock"); err != nil {
		return err
	}
	fmt.Println("  committed flake.lock")
	return nil
}
