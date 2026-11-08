package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

type Job struct {
	kind       string
	base       string
	candidates []string
	exts       []string
}

func newJob(kind, base string, urls ...string) Job {
	exts := make([]string, len(urls))
	for i, rawURL := range urls {
		exts[i] = urlExt(rawURL)
	}
	return Job{kind: kind, base: base, candidates: urls, exts: exts}
}

func urlExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	ext := strings.ToLower(path.Ext(u.Path))
	if ext == "" {
		return ".bin"
	}
	return ext
}

func PlanEmojiJobs(data *GuildData, dir string, force bool) ([]Job, int) {
	used := make(map[string]bool)
	var jobs []Job
	skipped := 0

	for _, emoji := range data.Emojis {
		if emoji.ID == "" {
			continue
		}

		base := uniqueBase(SanitizeName(emoji.Name), used)

		var candidates []string
		if emoji.Animated {
			candidates = []string{
				cdnBase + "/emojis/" + emoji.ID + ".gif",
				cdnBase + "/emojis/" + emoji.ID + ".webp?animated=true&quality=lossless",
				cdnBase + "/emojis/" + emoji.ID + ".webp?quality=lossless",
			}
		} else {
			candidates = []string{
				cdnBase + "/emojis/" + emoji.ID + ".webp?quality=lossless",
				cdnBase + "/emojis/" + emoji.ID + ".png",
			}
		}

		j := newJob("emoji", base, candidates...)
		if !force && anyExists(dir, j.base, j.exts) {
			skipped++
			continue
		}
		jobs = append(jobs, j)
	}

	return jobs, skipped
}

func PlanStickerJobs(data *GuildData, dir string, force bool) ([]Job, int) {
	used := make(map[string]bool)
	var jobs []Job
	skipped := 0

	for _, sticker := range data.Stickers {
		if sticker.ID == "" {
			continue
		}

		base := uniqueBase(SanitizeName(sticker.Name), used)

		var ext string
		switch sticker.FormatType {
		case 3:
			ext = ".json"
		case 4:
			ext = ".gif"
		case 2:
			// TODO: convert APNG stickers to GIF for viewers that don't animate PNGs.
			ext = ".png"
		default:
			ext = ".png"
		}

		j := newJob("sticker", base, mediaBase+"/stickers/"+sticker.ID+ext)
		if !force && anyExists(dir, j.base, j.exts) {
			skipped++
			continue
		}
		jobs = append(jobs, j)
	}

	return jobs, skipped
}

func DownloadJobs(ctx context.Context, client *Client, dir, label string, jobs []Job, workers int, quiet bool) []error {
	if len(jobs) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	progress := newProgress(label, len(jobs), quiet)
	defer progress.close()

	jobCh := make(chan Job)
	errCh := make(chan error, len(jobs))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				if err := downloadJob(ctx, client, dir, j); err != nil {
					errCh <- fmt.Errorf("%s %q: %w", j.kind, j.base, err)
				}
				progress.inc()
			}
		}()
	}

	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errs
}

func downloadJob(ctx context.Context, client *Client, dir string, j Job) error {
	body, ext, err := client.download(ctx, j.candidates)
	if err != nil {
		return err
	}
	defer body.Close()

	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_ = tmp.Chmod(0o644)

	_, copyErr := io.Copy(tmp, body)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpName)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}

	final := filepath.Join(dir, j.base+ext)
	if err := os.Rename(tmpName, final); err != nil {
		// Windows cannot rename over an existing file.
		if removeErr := os.Remove(final); removeErr == nil {
			err = os.Rename(tmpName, final)
		}
		if err != nil {
			_ = os.Remove(tmpName)
			return err
		}
	}
	return nil
}

func anyExists(dir, base string, exts []string) bool {
	for _, ext := range exts {
		info, err := os.Stat(filepath.Join(dir, base+ext))
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}

func uniqueBase(name string, used map[string]bool) string {
	if name == "" {
		name = "unnamed"
	}

	base := name
	for i := 2; ; i++ {
		key := strings.ToLower(base)
		if !used[key] {
			used[key] = true
			return base
		}
		base = fmt.Sprintf("%s_%d", name, i)
	}
}

func SanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed"
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 32 || r == 127:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	name = strings.TrimRight(b.String(), ". ")
	if name == "" {
		return "unnamed"
	}

	upper := strings.ToUpper(name)
	base := upper
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}

	reserved := map[string]struct{}{
		"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	}
	for i := 1; i <= 9; i++ {
		reserved[fmt.Sprintf("COM%d", i)] = struct{}{}
		reserved[fmt.Sprintf("LPT%d", i)] = struct{}{}
	}
	if _, ok := reserved[base]; ok {
		name = "_" + name
	}

	name = strings.TrimRight(truncateRunes(name, 100), ". ")
	if name == "" || name == "." || name == ".." {
		return "unnamed"
	}
	return name
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

type progress struct {
	label   string
	total   int64
	done    atomic.Int64
	enabled bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

var (
	barFilled = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	barEmpty  = lipgloss.NewStyle().Faint(true)
)

const barWidth = 20

func newProgress(label string, total int, quiet bool) *progress {
	p := &progress{
		label:   label,
		total:   int64(total),
		enabled: !quiet && total > 0 && term.IsTerminal(int(os.Stderr.Fd())),
		stop:    make(chan struct{}),
	}
	if !p.enabled {
		return p
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.print()
			case <-p.stop:
				p.print()
				return
			}
		}
	}()
	return p
}

func (p *progress) inc() {
	p.done.Add(1)
}

func (p *progress) print() {
	done := p.done.Load()
	filled := int(float64(done) / float64(p.total) * barWidth)
	bar := barFilled.Render(strings.Repeat("█", filled)) +
		barEmpty.Render(strings.Repeat("░", barWidth-filled))
	percent := int(float64(done) / float64(p.total) * 100)

	fmt.Fprintf(os.Stderr, "\r  %-9s %s %d/%d %3d%%", p.label, bar, done, p.total, percent)
}

func (p *progress) close() {
	if !p.enabled {
		return
	}
	close(p.stop)
	p.wg.Wait()
	fmt.Fprintln(os.Stderr)
}

func SaveGuildData(dir string, data *GuildData) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "guild.json"), b, 0o644)
}

func WriteErrors(dir string, errs []error) {
	path := filepath.Join(dir, "errors.log")
	if len(errs) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = os.WriteFile(path, []byte(errors.Join(errs...).Error()+"\n"), 0o644)
}
