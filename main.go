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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/FanaticExplorer/EmoteHarvest/internal/harvest"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
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
	styleLabel = lipgloss.NewStyle().Faint(true).Width(8)
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(harvest.ColorBlurple))
	styleHint  = lipgloss.NewStyle().Faint(true)
	styleGood  = lipgloss.NewStyle().Foreground(lipgloss.Color(harvest.ColorGreen))
	styleSkip  = lipgloss.NewStyle().Foreground(lipgloss.Color(harvest.ColorYellow))
	styleBad   = lipgloss.NewStyle().Foreground(lipgloss.Color(harvest.ColorRed))
)

var (
	interactive     bool
	pauseBeforeExit bool
)

var version = "dev"

func main() {
	_ = godotenv.Load()

	var opts options

	tokenFlag := flag.String("token", "", "Discord bot token (or set DISCORD_TOKEN)")
	flag.StringVar(&opts.out, "out", "output", "output directory")
	flag.IntVar(&opts.workers, "workers", 5, "concurrent downloads")
	flag.BoolVar(&opts.refresh, "refresh", false, "refetch guild metadata")
	flag.BoolVar(&opts.force, "force", false, "redownload files that already exist")
	flag.BoolVar(&opts.quiet, "quiet", false, "disable progress output")
	versionFlag := flag.Bool("version", false, "print version and exit")

	flag.Usage = usage

	_ = flag.CommandLine.Parse(hoistFlags(os.Args[1:]))

	if *versionFlag {
		fmt.Println(versionLine())
		return
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

	start := time.Now()

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

	fmt.Println()
	row("Guild", styleTitle.Render(folder))
	row("ID", styleHint.Render(guildID))
	row("Output", styleHint.Render(guildDir))
	fmt.Println()

	emojiJobs, emojiSkipped := harvest.PlanEmojiJobs(data, emojiDir, opts.force)
	stickerJobs, stickerSkipped := harvest.PlanStickerJobs(data, stickerDir, opts.force)

	emojiErrs := harvest.DownloadJobs(ctx, client, emojiDir, "emojis", emojiJobs, opts.workers, opts.quiet)
	stickerErrs := harvest.DownloadJobs(ctx, client, stickerDir, "stickers", stickerJobs, opts.workers, opts.quiet)

	allErrs := append(emojiErrs, stickerErrs...)
	harvest.WriteErrors(guildDir, allErrs)

	fmt.Println()
	row("Emojis", counts(len(emojiJobs)-len(emojiErrs), emojiSkipped, len(emojiErrs)))
	row("Stickers", counts(len(stickerJobs)-len(stickerErrs), stickerSkipped, len(stickerErrs)))
	row("Elapsed", styleHint.Render(formatDuration(time.Since(start))))
	if len(allErrs) > 0 {
		row("Errors", styleBad.Render(filepath.Join(guildDir, "errors.log")))
	}
	fmt.Println()

	pause()
}

func row(label, value string) {
	fmt.Printf("  %s %s\n", styleLabel.Render(label), value)
}

func discordTheme() *huh.Theme {
	blurple := lipgloss.Color(harvest.ColorBlurple)
	red := lipgloss.Color(harvest.ColorRed)
	green := lipgloss.Color(harvest.ColorGreen)

	t := huh.ThemeBase()

	t.Focused.Title = t.Focused.Title.Bold(true).Foreground(blurple)
	t.Focused.Description = t.Focused.Description.Faint(true)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(red)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(red)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(blurple)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(green)
	t.Focused.SelectedPrefix = t.Focused.SelectedPrefix.Foreground(green)
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(blurple)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(blurple)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Background(blurple).Foreground(lipgloss.Color("#FFFFFF"))
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(blurple)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(blurple)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Faint(true)

	t.Blurred = t.Focused
	t.Blurred.Title = t.Blurred.Title.Faint(true)
	t.Blurred.FocusedButton = t.Blurred.FocusedButton.Background(lipgloss.Color("#4E5058"))

	return t
}

func counts(downloaded, skipped, failed int) string {
	return fmt.Sprintf("%s downloaded · %s skipped · %s failed",
		styleGood.Render(strconv.Itoa(downloaded)),
		styleSkip.Render(strconv.Itoa(skipped)),
		styleBad.Render(strconv.Itoa(failed)))
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
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
	).WithTheme(discordTheme())
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

func usage() {
	out := flag.CommandLine.Output()
	name := filepath.Base(os.Args[0])

	fmt.Fprintf(out, "Usage: %s [flags] <invite-url|invite-code|guild-id> [token]\n\n", name)
	fmt.Fprintln(out, "Run without arguments for an interactive prompt.")
	fmt.Fprintln(out, "Exit codes: 0 success, 1 failure, 2 bad flags.")
	fmt.Fprintln(out, "\nExamples:")
	examples := [][2]string{
		{name, "interactive prompt"},
		{name + " archlinux", "invite code"},
		{name + " https://discord.gg/archlinux", "invite link"},
		{name + " 1145481614856556706", "server ID"},
		{name + " -force -workers 10 archlinux", "replace existing files"},
	}
	width := 0
	for _, example := range examples {
		if len(example[0]) > width {
			width = len(example[0])
		}
	}
	for _, example := range examples {
		fmt.Fprintf(out, "  %-*s  %s\n", width, example[0], example[1])
	}
	fmt.Fprintln(out, "\nToken, first match wins:")
	fmt.Fprintln(out, "  -token <token>, second positional argument, DISCORD_TOKEN, or .env")
	fmt.Fprintln(out, "\nFlags:")
	flag.PrintDefaults()
}

// hoistFlags moves flags that follow a positional argument in front of it, so
// argument order does not matter. Everything after a bare "--" is left alone.
func hoistFlags(args []string) []string {
	var flags, rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			rest = append(rest, arg)
			continue
		}

		flags = append(flags, arg)
		if flagTakesValue(arg) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}

	return append(flags, rest...)
}

func flagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if strings.Contains(name, "=") {
		return false
	}

	switch name {
	case "token", "out", "workers":
		return true
	}
	return false
}

func versionLine() string {
	out := "emoteharvest " + version

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}

	var details []string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev := setting.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
			details = append(details, "commit "+rev)
		case "vcs.time":
			details = append(details, "built "+setting.Value)
		}
	}
	if len(details) == 0 {
		return out
	}

	return out + " (" + strings.Join(details, ", ") + ")"
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
