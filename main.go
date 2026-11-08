package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/FanaticExplorer/EmoteHarvest/internal/harvest"
	"github.com/charmbracelet/huh"
	"github.com/joho/godotenv"
	"golang.org/x/term"
)

type options struct {
	server  string
	token   string
	out     string
	workers int
	refresh bool
	force   bool
	quiet   bool
}

var (
	interactive     bool
	pauseBeforeExit bool
)

func main() {
	_ = godotenv.Load()

	var opts options

	tokenFlag := flag.String("token", "", "Discord bot token (or set DISCORD_TOKEN)")
	flag.StringVar(&opts.out, "out", "output", "output directory")
	flag.IntVar(&opts.workers, "workers", 5, "concurrent downloads")
	flag.BoolVar(&opts.refresh, "refresh", false, "refetch guild metadata")
	flag.BoolVar(&opts.force, "force", false, "redownload files that already exist")
	flag.BoolVar(&opts.quiet, "quiet", false, "disable progress output")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage: %s [flags] <invite-url|invite-code|guild-id> [token]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(out, "Run without arguments for an interactive prompt.")
		fmt.Fprintln(out, "Token: -token, second positional argument, DISCORD_TOKEN, or .env.")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() > 1 && strings.HasPrefix(flag.Arg(1), "-") {
		fatal("flags must come before the invite/ID")
	}

	interactive = term.IsTerminal(int(os.Stdin.Fd()))
	pauseBeforeExit = interactive && flag.NArg() == 0

	opts.server = strings.TrimSpace(flag.Arg(0))
	opts.token = strings.TrimSpace(*tokenFlag)
	if opts.token == "" && flag.NArg() > 1 {
		opts.token = strings.TrimSpace(flag.Arg(1))
	}
	if opts.token == "" {
		opts.token = strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
	}

	if opts.token == "" && !interactive {
		missingTokenHelp()
	}

	if interactive && opts.server == "" {
		if err := runWizard(&opts); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				fmt.Fprintln(os.Stderr, "cancelled")
				os.Exit(0)
			}
			fatal("%v", err)
		}
	}

	if opts.server == "" && !interactive {
		fmt.Fprint(os.Stderr, "Guild invite or ID: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		opts.server = strings.TrimSpace(line)
	}
	if opts.server == "" {
		fatal("no invite or guild ID provided")
	}
	if opts.token == "" {
		missingTokenHelp()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := harvest.NewClient(opts.token)
	identity, err := client.Authenticate(ctx)
	if err != nil {
		fatal("%v", err)
	}
	if !identity.Bot {
		fmt.Fprintf(os.Stderr, "user token detected (%s), continuing with it; self-bot use violates Discord's ToS -- you take the responsibility\n", identity.Username)
	}

	guildID, inviteName, err := client.ResolveGuild(ctx, opts.server)
	if err != nil {
		fatal("%v", err)
	}

	root := filepath.Clean(opts.out)
	if root == "" {
		root = "output"
	}

	var data *harvest.GuildData
	if !opts.refresh {
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

	emojiJobs, emojiSkipped := harvest.PlanEmojiJobs(data, emojiDir, opts.force)
	stickerJobs, stickerSkipped := harvest.PlanStickerJobs(data, stickerDir, opts.force)

	emojiErrs := harvest.DownloadJobs(ctx, client, emojiDir, "emojis", emojiJobs, opts.workers, opts.quiet)
	stickerErrs := harvest.DownloadJobs(ctx, client, stickerDir, "stickers", stickerJobs, opts.workers, opts.quiet)

	allErrs := append(emojiErrs, stickerErrs...)
	harvest.WriteErrors(guildDir, allErrs)

	fmt.Printf("Emojis: %d downloaded, %d skipped, %d failed\n", len(emojiJobs)-len(emojiErrs), emojiSkipped, len(emojiErrs))
	fmt.Printf("Stickers: %d downloaded, %d skipped, %d failed\n", len(stickerJobs)-len(stickerErrs), stickerSkipped, len(stickerErrs))
	if len(allErrs) > 0 {
		fmt.Printf("Errors: %s\n", filepath.Join(guildDir, "errors.log"))
	}

	pause()
}

func runWizard(opts *options) error {
	server := opts.server
	token := opts.token
	workers := strconv.Itoa(opts.workers)

	target := []huh.Field{
		huh.NewInput().
			Title("Server").
			Description("Invite link, invite code, or server ID").
			Placeholder("https://discord.gg/archlinux").
			Value(&server),
	}
	if token == "" {
		target = append(target, huh.NewInput().
			Title("Token").
			Description("Bot token from the Discord developer dashboard; typing stays hidden").
			EchoMode(huh.EchoModePassword).
			Value(&token))
	}

	tuning := []huh.Field{
		huh.NewInput().
			Title("Output folder").
			Value(&opts.out),
		huh.NewInput().
			Title("Concurrent downloads").
			Value(&workers),
		huh.NewConfirm().
			Title("Refetch server metadata?").
			Description("Ignore the cached guild.json and ask Discord again").
			Value(&opts.refresh),
		huh.NewConfirm().
			Title("Re-download existing files?").
			Description("Otherwise files already on disk are skipped").
			Value(&opts.force),
		huh.NewConfirm().
			Title("Hide progress output?").
			Value(&opts.quiet),
	}

	form := huh.NewForm(
		huh.NewGroup(target...),
		huh.NewGroup(tuning...),
	)
	if err := form.Run(); err != nil {
		return err
	}

	opts.server = strings.TrimSpace(server)
	opts.token = strings.TrimSpace(token)
	opts.out = strings.TrimSpace(opts.out)
	if opts.out == "" {
		opts.out = "output"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(workers)); err == nil && n > 0 {
		opts.workers = n
	}
	return nil
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

func missingTokenHelp() {
	fmt.Fprintln(os.Stderr, "missing bot token")
	fmt.Fprintln(os.Stderr, "pass -token <token>, a second positional token, set DISCORD_TOKEN, or add DISCORD_TOKEN to .env")
	fmt.Fprintln(os.Stderr, "create a dummy bot: https://discord.com/developers/applications -> Bot -> Reset Token")
	fmt.Fprintln(os.Stderr, "for private servers, invite the bot to the server first")
	pause()
	os.Exit(1)
}

func pause() {
	if !pauseBeforeExit {
		return
	}
	fmt.Fprint(os.Stderr, "\nPress Enter to close...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	pause()
	os.Exit(1)
}
