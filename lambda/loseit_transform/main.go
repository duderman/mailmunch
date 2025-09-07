package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/snappy"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// s3API defines the subset of S3 methods used, to enable mocking in tests.
type s3API interface {
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

var newS3Client = func(ctx context.Context) (s3API, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg), nil
}

type LoseItLog struct {
	RecordType      *string  `parquet:"name=record_type, type=UTF8, repetitiontype=OPTIONAL"`
	Date            *string  `parquet:"name=date, type=UTF8, repetitiontype=OPTIONAL"`
	Meal            *string  `parquet:"name=meal, type=UTF8, repetitiontype=OPTIONAL"`
	Name            *string  `parquet:"name=name, type=UTF8, repetitiontype=OPTIONAL"`
	Quantity        *float64 `parquet:"name=quantity, type=DOUBLE, repetitiontype=OPTIONAL"`
	Units           *string  `parquet:"name=units, type=UTF8, repetitiontype=OPTIONAL"`
	Calories        *float64 `parquet:"name=calories, type=DOUBLE, repetitiontype=OPTIONAL"`
	ProteinG        *float64 `parquet:"name=protein_g, type=DOUBLE, repetitiontype=OPTIONAL"`
	FatG            *float64 `parquet:"name=fat_g, type=DOUBLE, repetitiontype=OPTIONAL"`
	CarbsG          *float64 `parquet:"name=carbs_g, type=DOUBLE, repetitiontype=OPTIONAL"`
	FiberG          *float64 `parquet:"name=fiber_g, type=DOUBLE, repetitiontype=OPTIONAL"`
	SodiumMg        *float64 `parquet:"name=sodium_mg, type=DOUBLE, repetitiontype=OPTIONAL"`
	SugarG          *float64 `parquet:"name=sugar_g, type=DOUBLE, repetitiontype=OPTIONAL"`
	DurationMinutes *float64 `parquet:"name=duration_minutes, type=DOUBLE, repetitiontype=OPTIONAL"`
	DistanceKm      *float64 `parquet:"name=distance_km, type=DOUBLE, repetitiontype=OPTIONAL"`
}

func main() { lambda.Start(handler) }

func handler(ctx context.Context, evt events.S3Event) error {
	bucketName := os.Getenv("DATA_BUCKET")
	if bucketName == "" {
		return fmt.Errorf("DATA_BUCKET env var is required")
	}

	rawCsvBase := envOr("RAW_CSV_BASE", "raw/loseit_csv/")
	curatedBase := envOr("CURATED_BASE", "curated/loseit_parquet/")

	s3c, err := newS3Client(ctx)
	if err != nil {
		return err
	}

	for _, rec := range evt.Records {
		b := rec.S3.Bucket.Name
		key := rec.S3.Object.Key
		if !strings.HasPrefix(key, rawCsvBase) {
			log.Printf("skip non-matching key: %s", key)
			continue
		}
		// Parse partition path: raw/loseit_csv/year=YYYY/month=MM/day=DD/...
		year, month, day := extractYMD(key)
		if year == "" {
			log.Printf("warn: cannot derive y/m/d from %s", key)
		}

		// Read CSV
		obj, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: &b, Key: &key})
		if err != nil {
			return fmt.Errorf("s3 get %s/%s: %w", b, key, err)
		}
		body, err := io.ReadAll(obj.Body)
		obj.Body.Close()
		if err != nil {
			return err
		}

		rows, err := parseCSV(body)
		if err != nil {
			return err
		}

		// Build Parquet in-memory
		buf := new(bytes.Buffer)
		w := parquet.NewWriter(buf,
			parquet.SchemaOf(new(LoseItLog)),
			parquet.Compression(&snappy.Codec{}),
		)
		// Map CSV rows to LoseItLog
		for _, r := range rows {
			rec := mapRow(r)
			if err := w.Write(rec); err != nil {
				return err
			}
		}
		if err := w.Close(); err != nil {
			return err
		}

		// Write Parquet to curated/loseit_parquet/year=YYYY/month=MM/day=DD/part-0000.snappy.parquet
		outKey := fmt.Sprintf("%syear=%s/month=%s/day=%s/part-0000.snappy.parquet", curatedBase, year, month, day)
		if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &bucketName,
			Key:         &outKey,
			Body:        bytes.NewReader(buf.Bytes()),
			ContentType: aws.String("application/octet-stream"),
			ACL:         s3types.ObjectCannedACLPrivate,
		}); err != nil {
			return err
		}
	}
	return nil
}

func parseCSV(b []byte) ([]map[string]string, error) {
	rdr := csv.NewReader(bytes.NewReader(b))
	rdr.TrimLeadingSpace = true
	rdr.ReuseRecord = false
	rdr.FieldsPerRecord = -1 // Allow variable number of fields
	hdr, err := rdr.Read()
	if err != nil {
		return nil, err
	}
	for i := range hdr {
		hdr[i] = norm(hdr[i])
	}
	var out []map[string]string
	for {
		rec, err := rdr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		row := map[string]string{}
		for i, v := range rec {
			if i < len(hdr) {
				row[hdr[i]] = strings.TrimSpace(v)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func mapRow(row map[string]string) *LoseItLog {
	// heuristics for LoseIt export headers
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := row[norm(k)]; ok {
				return v
			}
		}
		return ""
	}
	pfloat := func(s string) *float64 {
		if s == "" {
			return nil
		}
		f, err := parseFloat(s)
		if err != nil {
			return nil
		}
		return &f
	}
	pstr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}

	// Determine record type
	rt := get("record_type", "type")
	if rt == "" {
		// Default to "food", but check if this looks like exercise
		if get("type") == "Exercise" || strings.Contains(strings.ToLower(get("name")), "exercise") {
			rt = "exercise"
		} else {
			rt = "food"
		}
	}
	date := get("date")
	mealRaw := get("type") // LoseIt uses "Type" field for meal/category
	var meal *string
	if rt == "exercise" {
		meal = nil // No meal for exercise records
	} else {
		meal = pstr(mealRaw)
	}
	name := get("name", "food", "exercise")
	qty := pfloat(get("quantity", "amount"))
	units := pstr(get("units", "unit"))
	calories := pfloat(get("calories", "kcal"))
	protein := pfloat(get("protein_(g)", "protein"))
	fat := pfloat(get("fat_(g)", "fat"))
	carbs := pfloat(get("carbohydrates_(g)", "carbs", "carbohydrates"))
	fiber := pfloat(get("fiber_(g)", "fiber"))
	sodium := pfloat(get("sodium_(mg)", "sodium"))
	sugar := pfloat(get("sugars_(g)", "sugar"))
	duration := pfloat(get("duration_minutes", "duration"))
	distance := pfloat(get("distance_km", "distance"))

	return &LoseItLog{
		RecordType:      pstr(rt),
		Date:            pstr(date),
		Meal:            meal,
		Name:            pstr(name),
		Quantity:        qty,
		Units:           units,
		Calories:        calories,
		ProteinG:        protein,
		FatG:            fat,
		CarbsG:          carbs,
		FiberG:          fiber,
		SodiumMg:        sodium,
		SugarG:          sugar,
		DurationMinutes: duration,
		DistanceKm:      distance,
	}
}

func extractYMD(key string) (string, string, string) {
	// Expect .../year=YYYY/month=MM/day=DD/...
	segs := strings.Split(key, "/")
	var y, m, d string
	for _, s := range segs {
		if strings.HasPrefix(s, "year=") {
			y = strings.TrimPrefix(s, "year=")
		}
		if strings.HasPrefix(s, "month=") {
			m = strings.TrimPrefix(s, "month=")
		}
		if strings.HasPrefix(s, "day=") {
			d = strings.TrimPrefix(s, "day=")
		}
	}
	return y, m, d
}

func parseFloat(s string) (float64, error) {
	// strip commas and non-numeric except dot and minus
	re := regexp.MustCompile(`[^0-9.\-]+`)
	clean := re.ReplaceAllString(s, "")
	if clean == "" {
		return 0, fmt.Errorf("empty")
	}
	return strconv.ParseFloat(clean, 64)
}

func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))
}
