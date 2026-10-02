package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/karloscodes/matcha"
)

func TestApps(t *testing.T) {
	shop := matcha.AppConfig{
		Image: "chasen.invalid/shop:abc1234", Domain: "shop.example.com,www.example.com", Port: 3000,
		HealthPath: "/up", HealthTimeout: 60, Volumes: []string{"/app/storage", "/app/logs"},
		Env: map[string]string{"PRIVATE_KEY": "the-key-of-the-shop", "PORT": "3000"},
	}

	t.Run("the server keeps an app in its database, and in no file", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())

		err := saveApp("shop", shop)

		if err != nil {
			t.Fatal(err)
		}
		got, err := loadApp("shop")
		if err != nil || !reflect.DeepEqual(got, shop) {
			t.Errorf("got %+v (%v), want %+v", got, err, shop)
		}
		if _, err := os.Stat(appsPath()); !os.IsNotExist(err) {
			t.Errorf("the server made %s (%v)", appsPath(), err)
		}
	})

	t.Run("an app that is forgotten is not deployed", func(t *testing.T) {
		t.Setenv("CHASEN_ROOT", t.TempDir())
		saveApp("shop", shop)
		saveApp("blog", matcha.AppConfig{Image: "chasen.invalid/blog:1"})

		err := forgetApp("shop")

		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadApp("shop"); err == nil {
			t.Error("the server still has the shop")
		}
		if apps, _ := listApps(); len(apps) != 1 || apps["blog"].Image != "chasen.invalid/blog:1" {
			t.Errorf("want only the blog, got %+v", apps)
		}
	})
}

// A server from before kept its apps in apps.yml.
func TestAppsFromBefore(t *testing.T) {
	shop := matcha.AppConfig{Image: "chasen.invalid/shop:1", Domain: "shop.example.com", Port: 8080, HealthPath: "/up",
		Volumes: []string{"/app/storage"}, Env: map[string]string{"PRIVATE_KEY": "the-key-of-the-shop"}}
	serverFromBefore := func(t *testing.T) {
		t.Helper()
		t.Setenv("CHASEN_ROOT", t.TempDir())
		os.MkdirAll(filepath.Dir(appsPath()), 0755)
		if err := matcha.SaveAppTo(appsPath(), "shop", shop); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the server copies the apps, and the file stays as it was", func(t *testing.T) {
		serverFromBefore(t)
		before, _ := os.ReadFile(appsPath())

		got, err := loadApp("shop")

		if err != nil || !reflect.DeepEqual(got, shop) {
			t.Errorf("got %+v (%v), want %+v", got, err, shop)
		}
		if after, _ := os.ReadFile(appsPath()); string(after) != string(before) {
			t.Error("the file changed: the previous version cannot read its apps after an update that failed")
		}
	})

	t.Run("the file is copied one time: what the server does after that stays", func(t *testing.T) {
		serverFromBefore(t)
		loadApp("shop")
		newer := shop
		newer.Image = "chasen.invalid/shop:2"
		saveApp("shop", newer)
		saveApp("blog", matcha.AppConfig{Image: "chasen.invalid/blog:1"})

		err := forgetApp("blog")

		if err != nil {
			t.Fatal(err)
		}
		if apps, _ := listApps(); len(apps) != 1 || apps["shop"].Image != "chasen.invalid/shop:2" {
			t.Errorf("want the shop at version 2 and nothing else, got %+v", apps)
		}
	})

	t.Run("when the previous version writes the file again, the file is right", func(t *testing.T) {
		serverFromBefore(t)
		loadApp("shop")
		// An update failed and the previous version came back. It deploys.
		deployed := shop
		deployed.Image = "chasen.invalid/shop:2"
		matcha.SaveAppTo(appsPath(), "shop", deployed)
		later := time.Now().Add(time.Minute)
		os.Chtimes(appsPath(), later, later)

		got, err := loadApp("shop")

		if err != nil || got.Image != "chasen.invalid/shop:2" {
			t.Errorf("got %+v (%v), want version 2", got, err)
		}
	})

	t.Run("a file that the server cannot read is an error, not a server with no apps", func(t *testing.T) {
		serverFromBefore(t)
		os.WriteFile(appsPath(), []byte("apps: [not, a, map"), 0600)

		_, err := listApps()

		if err == nil {
			t.Fatal("want an error")
		}
	})
}
