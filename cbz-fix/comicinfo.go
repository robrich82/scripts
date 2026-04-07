package main

import "encoding/xml"

// ComicInfo represents the ComicInfo.xml schema (v2.1).
// Fields not listed here are not present in the standard and will be lost
// if the file is rewritten by this tool.
type ComicInfo struct {
	XMLName xml.Name `xml:"ComicInfo"`

	Title               string      `xml:"Title,omitempty"`
	Series              string      `xml:"Series,omitempty"`
	Number              string      `xml:"Number,omitempty"`
	Count               *int        `xml:"Count,omitempty"`
	Volume              *int        `xml:"Volume,omitempty"`
	AlternateSeries     string      `xml:"AlternateSeries,omitempty"`
	AlternateNumber     string      `xml:"AlternateNumber,omitempty"`
	StoryArc            string      `xml:"StoryArc,omitempty"`
	StoryArcNumber      string      `xml:"StoryArcNumber,omitempty"`
	SeriesGroup         string      `xml:"SeriesGroup,omitempty"`
	AlternateCount      *int        `xml:"AlternateCount,omitempty"`
	Summary             string      `xml:"Summary,omitempty"`
	Notes               string      `xml:"Notes,omitempty"`
	Year                *int        `xml:"Year,omitempty"`
	Month               *int        `xml:"Month,omitempty"`
	Day                 *int        `xml:"Day,omitempty"`
	Writer              string      `xml:"Writer,omitempty"`
	Penciller           string      `xml:"Penciller,omitempty"`
	Inker               string      `xml:"Inker,omitempty"`
	Colorist            string      `xml:"Colorist,omitempty"`
	Letterer            string      `xml:"Letterer,omitempty"`
	CoverArtist         string      `xml:"CoverArtist,omitempty"`
	Editor              string      `xml:"Editor,omitempty"`
	Translator          string      `xml:"Translator,omitempty"`
	Publisher           string      `xml:"Publisher,omitempty"`
	Imprint             string      `xml:"Imprint,omitempty"`
	Genre               string      `xml:"Genre,omitempty"`
	Tags                string      `xml:"Tags,omitempty"`
	Web                 string      `xml:"Web,omitempty"`
	PageCount           int         `xml:"PageCount"`
	LanguageISO         string      `xml:"LanguageISO,omitempty"`
	Format              string      `xml:"Format,omitempty"`
	BlackAndWhite       string      `xml:"BlackAndWhite,omitempty"`
	Manga               string      `xml:"Manga,omitempty"`
	Characters          string      `xml:"Characters,omitempty"`
	Teams               string      `xml:"Teams,omitempty"`
	Locations           string      `xml:"Locations,omitempty"`
	ScanInformation     string      `xml:"ScanInformation,omitempty"`
	AgeRating           string      `xml:"AgeRating,omitempty"`
	CommunityRating     string      `xml:"CommunityRating,omitempty"`
	MainCharacterOrTeam string      `xml:"MainCharacterOrTeam,omitempty"`
	Review              string      `xml:"Review,omitempty"`
	GTIN                string      `xml:"GTIN,omitempty"`
	Pages               *ComicPages `xml:"Pages,omitempty"`
}

// ComicPages is the container for the Pages element.
type ComicPages struct {
	Pages []ComicPage `xml:"Page"`
}

// ComicPage represents a single Page entry inside ComicInfo.xml.
type ComicPage struct {
	Image       int    `xml:"Image,attr"`
	Type        string `xml:"Type,attr,omitempty"`
	DoublePage  bool   `xml:"DoublePage,attr,omitempty"`
	ImageSize   int64  `xml:"ImageSize,attr,omitempty"`
	ImageWidth  int    `xml:"ImageWidth,attr,omitempty"`
	ImageHeight int    `xml:"ImageHeight,attr,omitempty"`
	Bookmark    string `xml:"Bookmark,attr,omitempty"`
}

func parseComicInfo(data []byte) (*ComicInfo, error) {
	var ci ComicInfo
	if err := xml.Unmarshal(data, &ci); err != nil {
		return nil, err
	}
	return &ci, nil
}

func marshalComicInfo(ci *ComicInfo) ([]byte, error) {
	data, err := xml.MarshalIndent(ci, "", "  ")
	if err != nil {
		return nil, err
	}
	out := append([]byte(xml.Header), data...)
	return out, nil
}
