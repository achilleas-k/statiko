package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
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

func checkError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func copyFile(srcName, dstName string) error {
	data, err := os.ReadFile(srcName)
	checkError(err)
	return os.WriteFile(dstName, data, 0666)
}

func readTemplate(templateFile string) string {
	thtml, err := os.ReadFile(templateFile)
	checkError(err)
	return string(thtml)
}

func makeHTML(data templateData, templateFile string) []byte {
	thtml := readTemplate(templateFile)
	t, err := template.New("webpage").Parse(thtml)
	checkError(err)
	rendered := new(bytes.Buffer)
	err = t.Execute(rendered, data)
	checkError(err)
	return rendered.Bytes()
}

func loadConfig() siteConfig {
	viper := viper.GetViper()
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetDefault("SiteName", "")
	viper.SetDefault("SourcePath", "pages-md")
	viper.SetDefault("DestinationPath", "html")
	viper.SetDefault("PageTemplateFile", "templates/template.html")
	viper.SetDefault("ResourcePath", "res")
	viper.SetDefault("PostPattern", `[0-9]{8}-.*`)
	err := viper.ReadInConfig()
	if err != nil && !strings.Contains(err.Error(), "Not Found") {
		checkError(err)
	}
	config := siteConfig{}
	checkError(viper.UnmarshalExact(&config))
	return config
}

func createDirs(conf siteConfig) {
	destpath := conf.DestinationPath
	checkError(os.MkdirAll(destpath, 0777))

	imagepath := path.Join(destpath, "images")
	checkError(os.MkdirAll(imagepath, 0777))

	respath := path.Join(destpath, "res")
	checkError(os.MkdirAll(respath, 0777))
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

func readPostMetadata(fname string) *postMetadata {
	// metadata files are stored next to each post but with the .meta.json extension
	fnameNoExt := strings.TrimSuffix(fname, filepath.Ext(fname))
	metadataPath := fnameNoExt + ".meta.json"

	if _, err := os.Stat(metadataPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	fp, err := os.Open(metadataPath)
	checkError(err)
	defer func() { checkError(fp.Close()) }()

	decoder := json.NewDecoder(fp)
	pm := &postMetadata{}
	checkError(decoder.Decode(pm))
	fmt.Printf("Decoded metadata: %+v\n", pm)
	return pm
}

func collectMarkdownFiles(srcpath string) []string {
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
	checkError(filepath.Walk(srcpath, mdfinder))
	return pagesmd
}

func plural(n int) string {
	if n != 1 {
		return "s"
	}
	return ""
}

func renderPages(conf siteConfig) {
	srcpath := conf.SourcePath

	sitename := conf.SiteName
	var data templateData

	data.SiteName = template.HTML(sitename)

	pagesmd := collectMarkdownFiles(srcpath)
	npages := len(pagesmd)
	pagelist := make([]string, npages)

	nposts := 0
	postlisting := make([]post, 0, npages)

	destpath := conf.DestinationPath
	templateFile := conf.PageTemplateFile
	postrePattern := conf.PostPattern
	fmt.Printf(":: Rendering %d page%s\n", npages, plural(npages))
	postre, err := regexp.Compile(postrePattern)
	checkError(err)

	htmlOpts := html.RendererOptions{}
	renderer := html.NewRenderer(htmlOpts)

	for idx, fname := range pagesmd {
		fmt.Printf("   %d: %s", idx+1, fname)
		pagemd, err := os.ReadFile(fname)
		checkError(err)

		// reverse render posts
		// data.Body[nposts-idx-1] = template.HTML(string(safe))
		doc := parseMD(pagemd)
		data.Body = template.HTML(markdown.Render(doc, renderer))

		// trim source path
		outpath := strings.TrimPrefix(fname, srcpath)
		// trim extension (and replace with .html)
		outpath = strings.TrimSuffix(outpath, filepath.Ext(outpath))
		outpath = fmt.Sprintf("%s.html", outpath)
		outpath = filepath.Join(destpath, outpath)

		// make potential parent directory
		outpathpar, _ := filepath.Split(outpath)
		if outpathpar != destpath {
			checkError(os.MkdirAll(outpathpar, 0777))
		}
		data.RelRoot, _ = filepath.Rel(outpathpar, destpath)

		checkError(os.WriteFile(outpath, makeHTML(data, templateFile), 0666))

		if postre.MatchString(fname) {
			p := parsePost(pagemd)
			postURL := strings.TrimPrefix(outpath, destpath)
			postURL = strings.TrimPrefix(postURL, "/") // make it relative
			p.url = postURL
			p.metadata = readPostMetadata(fname)
			postlisting = append(postlisting, p)
			nposts++
		}

		fmt.Printf(" -> %s\n", outpath)
		pagelist[idx] = outpath
	}
	fmt.Printf(":: Found %d posts\n", nposts)

	// render to listing page
	if nposts > 0 {
		// TODO: create listing page as ast instead of manually rendering blocks
		var bodystr string
		for idx, p := range postlisting {
			bodystr = fmt.Sprintf("%s%d. [%s](%s)\n    - %s\n", bodystr, idx, p.title, p.url, p.summary)
		}
		doc := parseMD([]byte(bodystr))
		data.Body = template.HTML(markdown.Render(doc, renderer))
		outpath := filepath.Join(destpath, "posts.html")
		fmt.Printf("   Saving posts: %s\n", outpath)
		err := os.WriteFile(outpath, makeHTML(data, templateFile), 0666)
		checkError(err)
	}
	fmt.Println(":: Rendering complete!")
}

// copyResources copies all files from the configured resource directory
// to the "res" subdirectory under the destination path.
func copyResources(conf siteConfig) {
	fmt.Println(":: Copying resources")
	dstroot := conf.DestinationPath
	walker := func(srcloc string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			dstloc := path.Join(dstroot, srcloc)
			fmt.Printf("   %s -> %s\n", srcloc, dstloc)
			checkError(copyFile(srcloc, dstloc))
		} else if info.Mode().IsDir() {
			dstloc := path.Join(dstroot, srcloc)
			fmt.Printf("   Creating directory %s\n", dstloc)
			checkError(os.MkdirAll(dstloc, 0777))
		}
		return nil
	}

	checkError(filepath.Walk(conf.ResourcePath, walker))
	fmt.Println("== Done ==")
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
	conf := loadConfig()
	createDirs(conf)
	renderPages(conf)
	copyResources(conf)
}
