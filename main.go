package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/FanaticExplorer/EmoteHarvest/internal/harvest"
)

func main() {
	tokenFlag := flag.String("token", "", "Discord bot token (or set DISCORD_TOKEN)")
	outRoot := flag.String("out", "output", "output directory")
	workers := flag.Int("workers", 5, "concurrent downloads")
	refresh := flag.Bool("refresh", false, "refetch guild metadata")
	force := flag.Bool("force", false, "redownload files that already exist")
	quiet := flag.Bool("quiet", false, "disable progress output")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <invite-url|invite-code|guild-id> [token]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(flag.CommandLine.Output(), "Token: -token, second positional argument, or DISCORD_TOKEN.")
		flag.PrintDefaults()
	}
	flag.Parse()

	input := strings.TrimSpace(flag.Arg(0))
	if input == "" {
		fmt.Fprint(os.Stderr, "Guild invite or ID: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		input = strings.TrimSpace(line)
	}
	if input == "" {
		fatal("no invite or guild ID provided")
	}

	if flag.NArg() > 1 && strings.HasPrefix(flag.Arg(1), "-") {
		fatal("flags must come before the invite/ID")
	}

	token := strings.TrimSpace(*tokenFlag)
	if token == "" && flag.NArg() > 1 {
		token = strings.TrimSpace(flag.Arg(1))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "missing bot token")
		fmt.Fprintln(os.Stderr, "pass -token <token>, a second positional token, or set DISCORD_TOKEN")
		fmt.Fprintln(os.Stderr, "create a dummy bot: https://discord.com/developers/applications -> Bot -> Reset Token")
		fmt.Fprintln(os.Stderr, "for private servers, invite the bot to the server first")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := harvest.NewClient(token)
	guildID, inviteName, err := client.ResolveGuild(ctx, input)
	if err != nil {
		fatal("%v", err)
	}

	root := filepath.Clean(*outRoot)
	if root == "" {
		root = "output"
	}

	var data *harvest.GuildData
	if !*refresh {
		data = findCachedGuild(root, guildID)
	}
	if data == nil {
		data, err = client.FetchGuildData(ctx, guildID, inviteName)
		if err != nil {
			fatal("%v", err)
		}
	}

	folderName := data.Name
	if folderName == "" {
		folderName = inviteName
	}
	if folderName == "" {
		folderName = guildID
	}
	folder := harvest.SanitizeName(folderName)

	guildDir := filepath.Join(root, folder)
	emojiDir := filepath.Join(guildDir, "emojis")
	stickerDir := filepath.Join(guildDir, "stickers")
	if err := os.MkdirAll(emojiDir, 0o755); err != nil {
		fatal("creating output directory: %v", err)
	}
	if err := os.MkdirAll(stickerDir, 0o755); err != nil {
		fatal("creating output directory: %v", err)
	}
	if err := harvest.SaveGuildData(guildDir, data); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not write guild.json:", err)
	}

	fmt.Printf("Guild: %s (%s)\n", folder, guildID)
	fmt.Printf("Output: %s\n", guildDir)

	emojiJobs, emojiSkipped := harvest.PlanEmojiJobs(data, emojiDir, *force)
	stickerJobs, stickerSkipped := harvest.PlanStickerJobs(data, stickerDir, *force)

	emojiErrs := harvest.DownloadJobs(ctx, client, emojiDir, "emojis", emojiJobs, *workers, *quiet)
	stickerErrs := harvest.DownloadJobs(ctx, client, stickerDir, "stickers", stickerJobs, *workers, *quiet)

	allErrs := append(emojiErrs, stickerErrs...)
	harvest.WriteErrors(guildDir, allErrs)

	fmt.Printf("Emojis: %d downloaded, %d skipped, %d failed\n", len(emojiJobs)-len(emojiErrs), emojiSkipped, len(emojiErrs))
	fmt.Printf("Stickers: %d downloaded, %d skipped, %d failed\n", len(stickerJobs)-len(stickerErrs), stickerSkipped, len(stickerErrs))
	if len(allErrs) > 0 {
		fmt.Printf("Errors: %s\n", filepath.Join(guildDir, "errors.log"))
	}
}

func findCachedGuild(root, guildID string) *harvest.GuildData {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := readGuildData(filepath.Join(root, entry.Name()))
		if err == nil && data.ID == guildID {
			return data
		}
	}
	return nil
}

func readGuildData(dir string) (*harvest.GuildData, error) {
	b, err := os.ReadFile(filepath.Join(dir, "guild.json"))
	if err != nil {
		return nil, err
	}

	var data harvest.GuildData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
