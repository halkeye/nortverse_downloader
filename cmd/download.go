package cmd

import (
	"archive/zip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/Jleagle/flaresolverr-go"
	"github.com/PuerkitoBio/goquery"
	"github.com/corpix/uarand"
	"github.com/fmartingr/go-comicinfo/v2"
	"github.com/spf13/cobra"
)

// downloadCmd represents the download command
var downloadCmd = &cobra.Command{
	Use:   "download",
	Short: "A brief description of your command",
	Run: func(cmd *cobra.Command, args []string) {
		single, err := cmd.PersistentFlags().GetBool("single")
		if err != nil {
			panic(err)
		}

		overwrite, err := cmd.PersistentFlags().GetBool("overwrite")
		if err != nil {
			panic(err)
		}

		nextUrl, err := cmd.PersistentFlags().GetString("start-url")
		if err != nil {
			panic(err)
		}

		outputDir, err := cmd.PersistentFlags().GetString("output")
		if err != nil {
			panic(err)
		}

		sleepMin, err := cmd.PersistentFlags().GetDuration("sleep-min")
		if err != nil {
			panic(err)
		}

		sleepMax, err := cmd.PersistentFlags().GetDuration("sleep-max")
		if err != nil {
			panic(err)
		}

		downloader := &downloader{
			client: &http.Client{
				Transport: &http.Transport{
					// https://old.reddit.com/r/redditdev/comments/uncu00/go_golang_clients_getting_403_blocked_responses/ says this will help with cloudflare
					TLSClientConfig: &tls.Config{},
				},
			},
			outputDir: outputDir,
			overwrite: overwrite,
		}

		flaresolverrURL, err := cmd.PersistentFlags().GetString("flaresolverr")
		if err != nil {
			panic(err)
		}
		if flaresolverrURL != "" {
			parsed, err := url.Parse(flaresolverrURL)
			if err != nil {
				panic(err)
			}

			downloader.client.Transport = flaresolverr.NewTransport(flaresolverr.NewClient(
				flaresolverr.WithHostName(parsed.Hostname()),
				flaresolverr.WithPortString(parsed.Port()),
				flaresolverr.WithProtocol(parsed.Scheme),
			))
		}
		for nextUrl != "" {
			url := nextUrl
			nextUrl, err = downloader.comic(cmd.Context(), url)
			if err != nil {
				panic(fmt.Errorf("%s - %w", url, err))
			}
			if single {
				break
			}
			sleepTime := time.Duration(rand.Intn(int(sleepMax)-int(sleepMin)) + int(sleepMin))
			slog.Info("sleeping", "sleepTime", sleepTime)
			time.Sleep(sleepTime)
		}
	},
}

type downloader struct {
	client    *http.Client
	outputDir string
	overwrite bool
}

func (d *downloader) url(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", uarand.GetRandom())
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("status code error: %d %s", res.StatusCode, res.Status)
	}
	return res.Body, nil
}

func (d *downloader) comic(ctx context.Context, comicURL string) (string, error) {
	var nextUrl string
	var comicID uint64

	body, err := d.url(ctx, comicURL)
	if err != nil {
		return "", fmt.Errorf("unable to download url: %w", err)
	}
	defer Checks(body.Close)

	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return "", fmt.Errorf("unable to read body: %w", err)
	}

	for _, s := range doc.Find("link[rel=shortlink]").EachIter() {
		if val, ok := s.Attr("href"); ok {
			u, err := url.Parse(val)
			if err != nil {
				return "", fmt.Errorf("unable to parse shortlink - %s: %w", val, err)
			}
			postID := u.Query().Get("p")
			comicID, err = strconv.ParseUint(postID, 10, 64)
			if err != nil {
				return "", fmt.Errorf("unable to get comicID from %s: %w", val, err)
			}
		}
	}

	err = os.MkdirAll(d.outputDir, 0750)
	if err != nil {
		return "", fmt.Errorf("unable to create output dir: %w", err)
	}

	cbzFilename := path.Join(d.outputDir, fmt.Sprintf("nortverse - %04d.cbz", comicID))

	for _, s := range doc.Find("a.next-comic").EachIter() {
		if val, ok := s.Attr("href"); ok {
			nextUrl = val
		}
	}

	if !d.overwrite {
		if _, err := os.Stat(cbzFilename); !errors.Is(err, os.ErrNotExist) {
			slog.Info("zip already exists, skipping", "filename", cbzFilename)
			return nextUrl, nil
		}
	}

	ci := comicinfo.NewComicInfo()
	ci.Series = "Nortverse"
	ci.Web = comicURL
	ci.LanguageISO = "en"
	ci.Format = "Web"

	for _, s := range doc.Find(".posted-on a").EachIter() {
		d, err := time.Parse("January 2, 2006", s.Text())
		if err != nil {
			return "", fmt.Errorf("unable parse date %s: %w", s.Text(), err)
		}
		ci.Year = d.Year()
		ci.Month = int(d.Month())
		ci.Day = d.Day()
	}

	pattern := regexp.MustCompile(`^\s*(.*)#(\d+)\s*$`)
	for _, s := range doc.Find(".default-lang .entry-title").EachIter() {
		ci.Title = s.Text()
		res := pattern.FindStringSubmatch(ci.Title)
		if len(res) > 0 {
			ci.StoryArc = res[1]
		}
	}

	for _, s := range doc.Find("a[href^='https://nortverse.com/comic-character/']").EachIter() {
		if ci.Characters != "" {
			ci.Characters = ci.Characters + ","
		}
		ci.Characters = ci.Characters + s.Text()
	}

	ci.Number = fmt.Sprint(comicID)

	slog.Info("creating zip", "filename", cbzFilename)
	zipFile, err := os.Create(cbzFilename)
	if err != nil {
		return "", fmt.Errorf("unable create zip file: %w", err)
	}
	defer Checks(zipFile.Close)

	zipWriter := zip.NewWriter(zipFile)
	defer Checks(zipWriter.Close)

	for i, s := range doc.Find("div#comic img").EachIter() {
		writer, err := zipWriter.Create(fmt.Sprintf("%04d.png", i+1))
		if err != nil {
			return "", fmt.Errorf("unable add file to zip: %w", err)
		}

		body, err := d.url(ctx, s.AttrOr("src", ""))
		if err != nil {
			return "", fmt.Errorf("downloading image: %w", err)
		}
		defer Checks(body.Close)

		// Write the file contents to the zip archive.
		_, err = io.Copy(writer, body)
		if err != nil {
			return "", fmt.Errorf("unable to add file contents to zip: %w", err)
		}
		ci.PageCount = i + 1
	}

	{
		writer, err := zipWriter.Create("ComicInfo.xml")
		if err != nil {
			return "", fmt.Errorf("unable add file to zip: %w", err)
		}

		err = comicinfo.Write(ci, writer)
		if err != nil {
			return "", fmt.Errorf("unable write file: %w", err)
		}
	}

	return nextUrl, nil
}

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.PersistentFlags().String("start-url", "https://nortverse.com/comic/overconfidence/", "start downloading from this url")
	downloadCmd.PersistentFlags().Bool("single", false, "only download the single issue")
	downloadCmd.PersistentFlags().Duration("sleep-min", 60*time.Second, "sleep between page downloads (min)")
	downloadCmd.PersistentFlags().Duration("sleep-max", 70*time.Second, "sleep between page downloads (max)")
	downloadCmd.PersistentFlags().Bool("overwrite", false, "even download if already exists")
	downloadCmd.PersistentFlags().String("output", "download", "download directory")
	downloadCmd.PersistentFlags().String("flaresolverr", "", "flaresolverr url")
}

func Checks(fs ...func() error) {
	for i := len(fs) - 1; i >= 0; i-- {
		if err := fs[i](); err != nil {
			slog.Error("Received error:", "err", err)
		}
	}
}
