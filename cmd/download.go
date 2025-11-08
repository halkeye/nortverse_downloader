package cmd

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/fmartingr/go-comicinfo/v2"
	"github.com/spf13/cobra"
)

// downloadCmd represents the download command
var downloadCmd = &cobra.Command{
	Use:   "download",
	Short: "A brief description of your command",
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		nextUrl := "https://nortverse.com/comic/overconfidence/"
		count := 1
		for nextUrl != "" {
			url := nextUrl
			nextUrl, err = downloadComic(cmd.Context(), count, url)
			if err != nil {
				panic(fmt.Errorf("unable to download url: %s - %w", url, err))
			}
			count++
			time.Sleep(time.Second * 5)
		}
	},
}

func downloadUrl(ctx context.Context, url string) (io.ReadCloser, error) {
	fmt.Println(url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req.Header.Set("User-Agent", "nortverse-downloader/1.0.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unable to download url: %w", err)
	}

	if res.StatusCode != 200 {
		return nil, fmt.Errorf("status code error: %d %s", res.StatusCode, res.Status)
	}
	return res.Body, nil
}

func downloadComic(ctx context.Context, count int, url string) (string, error) {
	var nextUrl string
	cbzFilename := fmt.Sprintf("download/nortverse - %04d.cbz", count)

	body, err := downloadUrl(ctx, url)
	defer body.Close()
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return "", fmt.Errorf("unable to read body: %w", err)
	}

	for _, s := range doc.Find("a.next-comic").EachIter() {
		if val, ok := s.Attr("href"); ok {
			nextUrl = val
		}
	}

	if _, err := os.Stat(cbzFilename); !errors.Is(err, os.ErrNotExist) {
		fmt.Printf("%s already exists\n", cbzFilename)
		return nextUrl, nil
	}

	ci := comicinfo.NewComicInfo()
	ci.Series = "Nortverse"
	ci.Web = url
	ci.LanguageISO = "en"
	ci.Format = "Web"

	for _, s := range doc.Find(".posted-on a").EachIter() {
		d, err := time.Parse("January 2, 2006", s.Text())
		if err != nil {
			fmt.Println(err)
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
			ci.StoryArc = res[0]
		}
	}

	for _, s := range doc.Find("a[href^='https://nortverse.com/comic-character/']").EachIter() {
		if ci.Characters != "" {
			ci.Characters = ci.Characters + ","
		}
		ci.Characters = ci.Characters + s.Text()
	}

	ci.Number = fmt.Sprint(count)

	// Create a new zip archive.
	zipFile, err := os.Create(cbzFilename)
	if err != nil {
		return "", fmt.Errorf("unable create zip file: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for i, s := range doc.Find("div#comic img").EachIter() {
		writer, err := zipWriter.Create(fmt.Sprintf("%04d.png", i+1))
		if err != nil {
			return "", fmt.Errorf("unable add file to zip: %w", err)
		}

		body, err := downloadUrl(ctx, s.AttrOr("src", ""))
		if err != nil {
			return "", fmt.Errorf("downloading image: %w", err)
		}
		defer body.Close()

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

		comicinfo.Write(ci, writer)
	}

	return nextUrl, nil
}

func init() {
	rootCmd.AddCommand(downloadCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// downloadCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// downloadCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
