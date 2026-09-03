package smoke

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// This checks the repository's Fiber dependency without starting a server or
// implementing an application feature.
func TestFiberDependency(t *testing.T) {
	app := fiber.New()
	app.Get("/smoke", func(c fiber.Ctx) error {
		return c.SendString("go-blocks")
	})

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/smoke", nil))
	if err != nil {
		t.Fatalf("Fiber request: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "go-blocks" {
		t.Fatalf("unexpected response: status=%d body=%q", response.StatusCode, body)
	}
}
