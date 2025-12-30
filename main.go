package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/html"
	"github.com/gomarkdown/markdown/parser"
	"github.com/spf13/viper"
)

var (
	build  string
	commit string
	verstr string
)

type siteConfig struct {
	SiteName         string `mapstructure:"SiteName"`
	SourcePath       string `mapstructure:"SourcePath"`
	DestinationPath  string `mapstructure:"DestinationPath"`
	PageTemplateFile string `mapstructure:"PageTemplateFile"`
	ResourcePath     string `mapstructure:"ResourcePath"`
	PostPattern      string `mapstructure:"PostPattern"`
}

type templateData struct {
	SiteName template.HTML
	Body     template.HTML
	// RelRoot is a relative path prefix that points to the root of the HTML destination directory.
	// It can be used to make relative links to pages and resources.
	RelRoot string
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
	os.Exit(1)
}

func copyFile(srcName, dstName string) error {
	data, err := os.ReadFile(srcName)
	if err != nil {
		return fmt.Errorf("reading file %q for copy: %w", srcName, err)
	}
	if err := os.WriteFile(dstName, data, 0666); err != nil {
		return fmt.Errorf("writing file %q for copy: %w", dstName, err)
	}
	return nil
}

func readTemplate(templateFile string) (string, error) {
	thtml, err := os.ReadFile(templateFile)
	if err != nil {
		return "", fmt.Errorf("reading template file %q: %w", templateFile, err)
	}
	return string(thtml), nil
}

func makeHTML(data templateData, templateFile string) ([]byte, error) {
	thtml, err := readTemplate(templateFile)
	if err != nil {
		return nil, fmt.Errorf("making HTML: %w", err)
	}
	t, err := template.New("webpage").Parse(thtml)
	if err != nil {
		return nil, fmt.Errorf("making HTML: %w", err)
	}
	rendered := new(bytes.Buffer)
	if err := t.Execute(rendered, data); err != nil {
		return nil, fmt.Errorf("making HTML: %w", err)
	}
	return rendered.Bytes(), nil
}

func loadConfig() (siteConfig, error) {
	viper := viper.GetViper()
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetDefault("SiteName", "")
	viper.SetDefault("SourcePath", "pages-md")
	viper.SetDefault("DestinationPath", "html")
	viper.SetDefault("PageTemplateFile", "templates/template.html")
	viper.SetDefault("ResourcePath", "res")
	viper.SetDefault("PostPattern", `[0-9]{8}-.*`)
	if err := viper.ReadInConfig(); err != nil {
		return siteConfig{}, fmt.Errorf("loading config: %w", err)
	}
	config := siteConfig{}
	if err := viper.UnmarshalExact(&config); err != nil {
		return siteConfig{}, fmt.Errorf("loading config: %w", err)
	}
	return config, nil
}

func createDirs(conf siteConfig) error {
	destpath := conf.DestinationPath
	if err := os.MkdirAll(destpath, 0777); err != nil {
		return fmt.Errorf("creating destination path %q: %w", destpath, err)
	}

	imagepath := path.Join(destpath, "images")
	if err := os.MkdirAll(imagepath, 0777); err != nil {
		return fmt.Errorf("creating image path %q: %w", imagepath, err)
	}

	respath := path.Join(destpath, "res")
	if err := os.MkdirAll(respath, 0777); err != nil {
		return fmt.Errorf("creating resource path %q: %w", respath, err)
	}

	return nil
}

type postMetadata struct {
	DatePosted  time.Time   `json:"posted"`
	DatesEdited []time.Time `json:"edited"`
}

type post struct {
	title   string
	summary string
	url     string

	metadata *postMetadata
}

// childLiterals concatenates the literals under a given node into a single
// string.
func childLiterals(node ast.Node) string {
	var lit string
	for _, child := range node.GetChildren() {
		if leafNode := child.AsLeaf(); leafNode != nil {
			lit += string(leafNode.Literal)
		} else {
			lit += childLiterals(child)
		}
	}
	return lit
}

func parsePost(mdsource []byte) post {
	var p post
	rootnode := parseMD(mdsource)
	visitor := func(node ast.Node, _ bool) ast.WalkStatus {
		switch nd := node.(type) {
		case *ast.Heading:
			if nd.Level == 1 && p.title == "" {
				p.title = childLiterals(node)
			}
		case *ast.Paragraph:
			// Found first paragraph
			p.summary = childLiterals(node)
			return ast.Terminate
		}
		return ast.GoToNext
	}
	ast.WalkFunc(rootnode, visitor)
	return p
}

func parseMD(md []byte) ast.Node {
	// each Parse call requires a new parser
	mdparser := parser.NewWithExtensions(parser.CommonExtensions | parser.AutoHeadingIDs)
	return mdparser.Parse(md)
}

func readPostMetadata(fname string) (*postMetadata, error) {
	// metadata files are stored next to each post but with the .meta.json extension
	fnameNoExt := strings.TrimSuffix(fname, filepath.Ext(fname))
	metadataPath := fnameNoExt + ".meta.json"

	if _, err := os.Stat(metadataPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	fp, err := os.Open(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("reading post metadata %q: %w", metadataPath, err)
	}
	defer func() {
		if err := fp.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "closing file after reading post metadata: %v", err)
		}
	}()

	decoder := json.NewDecoder(fp)
	pm := &postMetadata{}
	if err := decoder.Decode(pm); err != nil {
		return nil, fmt.Errorf("reading post metadata %q: %w", metadataPath, err)
	}
	return pm, nil
}

func collectMarkdownFiles(srcpath string) ([]string, error) {
	var pagesmd []string
	mdfinder := func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if filepath.Ext(path) == ".md" {
			pagesmd = append(pagesmd, path)
		}
		return nil
	}
	if err := filepath.Walk(srcpath, mdfinder); err != nil {
		return nil, fmt.Errorf("collecting markdown files in %q: %w", srcpath, err)
	}
	return pagesmd, nil
}

func plural(n int) string {
	if n != 1 {
		return "s"
	}
	return ""
}

func renderPostsPage(posts []post, data templateData, renderer *html.Renderer, templateFile, destpath string) error {
	fmt.Printf(":: Found %d posts\n", len(posts))

	// render to listing page
	if len(posts) > 0 {
		// TODO: create listing page as ast instead of manually rendering blocks
		var bodystr string
		for idx, p := range posts {
			bodystr = fmt.Sprintf("%s%d. [%s](%s)\n    - %s\n", bodystr, idx, p.title, p.url, p.summary)
		}
		doc := parseMD([]byte(bodystr))
		data.Body = template.HTML(markdown.Render(doc, renderer))
		outpath := filepath.Join(destpath, "posts.html")
		fmt.Printf("   Saving posts: %s\n", outpath)
		htmlData, err := makeHTML(data, templateFile)
		if err != nil {
			return fmt.Errorf("making html for posts page: %w", err)
		}
		if err := os.WriteFile(outpath, htmlData, 0666); err != nil {
			return fmt.Errorf("writing posts page %q: %w", outpath, err)
		}
	}
	return nil
}

func addDate(doc ast.Node, p post) {
	// add posted date to the end of the post
	dateStr := p.metadata.DatePosted.Format(time.RFC1123)
	footer := fmt.Sprintf("Posted: %s", dateStr)
	hr := ast.HorizontalRule{}
	dateParagraph := ast.Paragraph{}
	ast.AppendChild(&dateParagraph, &ast.Text{
		Leaf: ast.Leaf{
			Literal: []byte(footer),
		},
	})
	ast.AppendChild(doc, &hr)
	ast.AppendChild(doc, &dateParagraph)
}

func renderPages(conf siteConfig) error {
	srcpath := conf.SourcePath

	sitename := conf.SiteName
	var data templateData

	data.SiteName = template.HTML(sitename)

	pagesmd, err := collectMarkdownFiles(srcpath)
	if err != nil {
		return fmt.Errorf("rendering pages: %w", err)
	}
	npages := len(pagesmd)
	pagelist := make([]string, npages)

	destpath := conf.DestinationPath
	templateFile := conf.PageTemplateFile
	postrePattern := conf.PostPattern
	fmt.Printf(":: Rendering %d page%s\n", npages, plural(npages))
	postre, err := regexp.Compile(postrePattern)
	if err != nil {
		return fmt.Errorf("rendering pages: %w", err)
	}

	htmlOpts := html.RendererOptions{}
	renderer := html.NewRenderer(htmlOpts)

	posts := make([]post, 0, npages)

	for idx, fname := range pagesmd {
		fmt.Printf("   %d: %s", idx+1, fname)

		// trim source path
		outpath := strings.TrimPrefix(fname, srcpath)
		// trim extension (and replace with .html)
		outpath = strings.TrimSuffix(outpath, filepath.Ext(outpath))
		outpath = fmt.Sprintf("%s.html", outpath)
		outpath = filepath.Join(destpath, outpath)
		pagemd, err := os.ReadFile(fname)
		if err != nil {
			return fmt.Errorf("rendering pages: reading file %q: %w", fname, err)
		}

		doc := parseMD(pagemd)
		if postre.MatchString(fname) {
			p := parsePost(pagemd)
			postURL := strings.TrimPrefix(outpath, destpath)
			postURL = strings.TrimPrefix(postURL, "/") // make it relative
			p.url = postURL
			metadata, err := readPostMetadata(fname)
			if err != nil {
				return fmt.Errorf("rendering pages: %w", err)
			}
			p.metadata = metadata
			posts = append(posts, p)

			addDate(doc, p)
		}

		// reverse render posts
		// data.Body[nposts-idx-1] = template.HTML(string(safe))
		data.Body = template.HTML(markdown.Render(doc, renderer))

		// make potential parent directory
		outpathpar, _ := filepath.Split(outpath)
		if outpathpar != destpath {
			if err := os.MkdirAll(outpathpar, 0777); err != nil {
				return fmt.Errorf("rendering pages: creating path %q: %w", outpathpar, err)
			}
		}
		data.RelRoot, _ = filepath.Rel(outpathpar, destpath)

		htmlData, err := makeHTML(data, templateFile)
		if err != nil {
			return fmt.Errorf("rending pages: %w", err)
		}

		if err := os.WriteFile(outpath, htmlData, 0666); err != nil {
			return fmt.Errorf("rendering pages: writing html file %q: %w", outpath, err)
		}

		fmt.Printf(" -> %s\n", outpath)
		pagelist[idx] = outpath
	}
	renderPostsPage(posts, data, renderer, templateFile, destpath)
	fmt.Println(":: Rendering complete!")
	return nil
}

// copyResources copies all files from the configured resource directory
// to the "res" subdirectory under the destination path.
func copyResources(conf siteConfig) error {
	fmt.Println(":: Copying resources")
	dstroot := conf.DestinationPath
	walker := func(srcloc string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			dstloc := path.Join(dstroot, srcloc)
			fmt.Printf("   %s -> %s\n", srcloc, dstloc)
			if err := copyFile(srcloc, dstloc); err != nil {
				return fmt.Errorf("copying resources: %w", err)
			}
		} else if info.Mode().IsDir() {
			dstloc := path.Join(dstroot, srcloc)
			fmt.Printf("   Creating directory %s\n", dstloc)
			if err := os.MkdirAll(dstloc, 0777); err != nil {
				return fmt.Errorf("copying resources: creating path %q: %w", dstloc, err)
			}
		}
		return nil
	}

	if err := filepath.Walk(conf.ResourcePath, walker); err != nil {
		return err
	}
	fmt.Println("== Done ==")
	return nil
}

func printversion() {
	fmt.Println(verstr)
}

func init() {
	if build == "" {
		verstr = "statiko [dev build]"
	} else {
		verstr = fmt.Sprintf("statiko Build %s (%s)", build, commit)
	}
}

func main() {
	var printver bool
	flag.BoolVar(&printver, "version", false, "print version number")
	flag.Parse()
	if printver {
		printversion()
		return
	}
	conf, err := loadConfig()
	if err != nil {
		die("error: %v", err)
	}
	if err := createDirs(conf); err != nil {
		die("error: %v", err)
	}

	if err := renderPages(conf); err != nil {
		die("error: %v", err)
	}
	if err := copyResources(conf); err != nil {
		die("error: %v", err)
	}
}
