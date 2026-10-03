package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBucketUsage(t *testing.T) {
	now := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	const mb = 1 << 20

	t.Run("it says what each app holds, and what the replica is made of", func(t *testing.T) {
		objects := []s3Object{
			{"shop/snapshots/20261002T180000Z/storage/db.sqlite3.gz", 20 * mb, at(time.Hour)},
			{"shop/snapshots/20261002T190000Z/storage/db.sqlite3.gz", 20 * mb, at(time.Minute)},
			{"shop/live/storage/db.sqlite3/0000/0000000000000050-0000000000000050.ltx", 12 << 10, at(time.Minute)},
			{"shop/live/storage/db.sqlite3/0001/0000000000000001-0000000000000006.ltx", 45 * mb, at(18 * time.Minute)},
			{"shop/live/storage/db.sqlite3/0001/0000000000000041-0000000000000044.ltx", 35 << 10, at(4 * time.Minute)},
			{"shop/live/storage/db.sqlite3/0002/0000000000000001-0000000000000044.ltx", 45 * mb, at(3 * time.Minute)},
			{"blog/live/storage/blog.db/0009/0000000000000001-0000000000000090.ltx", 2 * mb, at(5 * time.Hour)},
		}

		got := bucketUsage(objects, now)

		want := `APP   SNAPSHOTS             LIVE REPLICA  LAST COPIED CHANGE  TOTAL
blog  none                  2.0 MB        5 hours ago         2.0 MB
shop  40.0 MB in 2 backups  90.0 MB       1 minute ago        130.0 MB
Total: 132.0 MB in 7 objects

The live replica is files of changes. What it holds now:
  changes, as they happen    1 file   12 KB    kept 5 minutes
  merged each 30 seconds     2 files  45.0 MB
  merged each 5 minutes      1 file   45.0 MB
  merged each hour           0 files  0
  full copies, one each day  1 file   2.0 MB   kept 1 day
Its oldest file is 5 hours old. After each daily full copy, the full copy of the day before and the files of changes from before it are deleted: the replica holds one full copy and the changes of one to two days, not more. The first cleanup is at the second midnight.
LAST COPIED CHANGE is the newest file of the replica. An app that writes all the time shows seconds. To prove that a replica restores: chasen verify
`
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a bucket with snapshots only has no part about the replica", func(t *testing.T) {
		got := bucketUsage([]s3Object{{"shop/snapshots/20261002T190000Z/storage/db.sqlite3.gz", 3 * mb, at(time.Minute)}}, now)

		want := "APP   SNAPSHOTS           LIVE REPLICA  LAST COPIED CHANGE  TOTAL\nshop  3.0 MB in 1 backup  0             -                   3.0 MB\nTotal: 3.0 MB in 1 object\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("files that are not from Chasen count in the total, and it says so", func(t *testing.T) {
		got := bucketUsage([]s3Object{{"photos/cat.jpg", 5 * mb, at(time.Hour)}}, now)

		want := "APP  SNAPSHOTS  LIVE REPLICA  LAST COPIED CHANGE  TOTAL\nTotal: 5.0 MB in 1 object. 5.0 MB of it is not from Chasen\n"
		if got != want {
			t.Errorf("got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("an empty bucket says so", func(t *testing.T) {
		if got := bucketUsage(nil, now); got != "The bucket is empty.\n" {
			t.Errorf("got %q", got)
		}
	})
}

// A store that answers a listing in two pages, like a bucket with more than
// 1000 objects.
func TestObjectsOfTheBucket(t *testing.T) {
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken>
<Contents><Key>shop/live/db/0001/a.ltx</Key><LastModified>2026-10-02T18:51:31.000Z</LastModified><Size>46950400</Size></Contents></ListBucketResult>`
		if r.URL.Query().Get("continuation-token") == "next" {
			page = `<ListBucketResult><IsTruncated>false</IsTruncated>
<Contents><Key>shop/snapshots/20261002T190000Z/db.gz</Key><LastModified>2026-10-02T19:00:02.000Z</LastModified><Size>21000</Size></Contents></ListBucketResult>`
		}
		fmt.Fprint(w, page)
	}))
	defer store.Close()
	s3 := &s3Config{Endpoint: store.URL, Region: "auto", Bucket: "backups", AccessKeyID: "id", SecretAccessKey: "secret"}

	objects, err := s3.objects("")

	if err != nil || len(objects) != 2 {
		t.Fatalf("objects = %+v (%v), want the objects of both pages", objects, err)
	}
	if first := objects[0]; first.Key != "shop/live/db/0001/a.ltx" || first.Size != 46950400 || !first.LastModified.Equal(time.Date(2026, 10, 2, 18, 51, 31, 0, time.UTC)) {
		t.Errorf("the first object = %+v, want its key, its size, and its time", first)
	}
}
