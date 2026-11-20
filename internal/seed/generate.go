// Package seed generates the mock users the campaigns are sent to. Everything is
// fake: names from fixed lists, emails on reserved example domains (RFC 2606),
// and phone numbers in the Turkish mobile format.
//
// Generation is deterministic: the same Options always give the same users, so
// tests and demo numbers are reproducible.
package seed

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

type User struct {
	FirstName      string
	LastName       string
	Email          string
	Phone          string
	City           string
	Language       string // "tr" or "en"
	SMSConsent     bool
	EmailConsent   bool
	OrderCount     int
	TotalSpent     float64 // TRY, two decimals
	LastPurchaseAt *time.Time
	LastCartAt     *time.Time
	SignedUpAt     time.Time
}

type Options struct {
	N    int       // number of users
	Seed uint64    // same seed, same users
	Now  time.Time // reference time for all dates
}

// Share of users per trait. Tuned so the example brief ("Istanbul, added to cart
// but didn't buy in the last 30 days") matches a few hundred of 5,000 users.
const (
	englishShare     = 0.15
	smsConsentShare  = 0.70
	mailConsentShare = 0.80
	noOrdersShare    = 0.30
	cartShare        = 0.50 // users with any cart activity (within 90 days)
	boughtAfterCart  = 0.40 // of cart users with orders: the cart turned into a purchase
	badContactShare  = 0.005
)

var cities = []struct {
	name   string
	weight int
}{
	{"İstanbul", 35}, {"Ankara", 15}, {"İzmir", 12}, {"Bursa", 7}, {"Antalya", 7},
	{"Adana", 5}, {"Konya", 5}, {"Gaziantep", 4}, {"Kocaeli", 4}, {"Eskişehir", 3}, {"Trabzon", 3},
}

// Cities lists the city names the seed uses, for the segment engine's
// allowlist.
func Cities() []string {
	out := make([]string, len(cities))
	for i, c := range cities {
		out[i] = c.name
	}
	return out
}

var (
	trFirst = []string{
		"Ahmet", "Mehmet", "Mustafa", "Ali", "Hüseyin", "Hasan", "İbrahim", "Emre", "Murat", "Burak",
		"Can", "Cem", "Deniz", "Efe", "Kerem", "Ozan", "Onur", "Serkan", "Tolga", "Yusuf",
		"Barış", "Oğuz", "Kaan", "Arda", "Eren", "Abdurrahman",
		"Ayşe", "Fatma", "Emine", "Hatice", "Zeynep", "Elif", "Merve", "Büşra", "Esra", "Selin",
		"Ece", "Defne", "İrem", "Gizem", "Derya", "Seda", "Pınar", "Özlem", "Gül", "Ebru",
		"Aslı", "Nazlı", "Buse", "Damla", "Cansu", "Gülizar",
	}
	trLast = []string{
		"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Yıldırım", "Öztürk", "Aydın", "Özdemir",
		"Arslan", "Doğan", "Kılıç", "Aslan", "Çetin", "Kara", "Koç", "Kurt", "Özkan", "Şimşek",
		"Polat", "Güneş", "Aksoy", "Bulut", "Korkmaz", "Tekin", "Uçar", "Karaca", "Akın", "Karaoğlu",
	}
	enFirst = []string{
		"Emma", "Olivia", "Liam", "Noah", "James", "Sophia", "Oliver", "Amelia",
		"Jack", "Mia", "Harry", "Grace", "Thomas", "Chloe", "Daniel", "Lucy",
	}
	enLast = []string{
		"Smith", "Johnson", "Brown", "Taylor", "Wilson", "Davies",
		"Evans", "Walker", "Wright", "Clarke", "Hughes", "Green",
	}
	emailDomains = []string{"example.com", "example.org", "example.net"}
)

const day = 24 * time.Hour

// Generate returns opt.N users.
func Generate(opt Options) []User {
	rng := rand.New(rand.NewPCG(opt.Seed, opt.Seed^0x9e3779b97f4a7c15))
	now := opt.Now.UTC().Truncate(time.Second)
	usedEmails := map[string]int{}

	users := make([]User, 0, opt.N)
	for range opt.N {
		u := User{Language: "tr"}
		first, last := trFirst, trLast
		if rng.Float64() < englishShare {
			u.Language = "en"
			first, last = enFirst, enLast
		}
		u.FirstName = first[rng.IntN(len(first))]
		u.LastName = last[rng.IntN(len(last))]
		u.City = pickCity(rng)
		u.SMSConsent = rng.Float64() < smsConsentShare
		u.EmailConsent = rng.Float64() < mailConsentShare
		u.Email = uniqueEmail(rng, usedEmails, u.FirstName, u.LastName)
		u.Phone = phone(rng)
		if rng.Float64() < badContactShare {
			u.Phone = "+90555" + digits(rng, 2) // too short: the provider rejects it
		}
		if rng.Float64() < badContactShare {
			u.Email = strings.Replace(u.Email, "@", ".", 1) // no @: the provider rejects it
		}

		u.SignedUpAt = now.Add(-time.Duration(rng.Int64N(int64(3 * 365 * day)))).Truncate(time.Second)

		if rng.Float64() >= noOrdersShare {
			u.OrderCount = 1 + int(rng.ExpFloat64()*4)
			if u.OrderCount > 40 {
				u.OrderCount = 40
			}
			total := 0.0
			for range u.OrderCount {
				total += 80 + rng.Float64()*1120
			}
			u.TotalSpent = math.Round(total*100) / 100
			ago := time.Duration(rng.ExpFloat64() * float64(60*day))
			u.LastPurchaseAt = clampAfter(now.Add(-ago), u.SignedUpAt)
		}

		if rng.Float64() < cartShare {
			// Recent activity is more common: exponential ages, mean 30 days, capped at 90.
			ago := min(time.Duration(rng.ExpFloat64()*float64(30*day)), 90*day)
			cart := clampAfter(now.Add(-ago), u.SignedUpAt)
			u.LastCartAt = cart
			// Some carts end in a purchase shortly after.
			if u.OrderCount > 0 && rng.Float64() < boughtAfterCart {
				bought := cart.Add(time.Duration(rng.Int64N(int64(2 * day))))
				if bought.Before(now) {
					u.LastPurchaseAt = &bought
				}
			}
		}
		users = append(users, u)
	}
	return users
}

func pickCity(rng *rand.Rand) string {
	total := 0
	for _, c := range cities {
		total += c.weight
	}
	n := rng.IntN(total)
	for _, c := range cities {
		if n < c.weight {
			return c.name
		}
		n -= c.weight
	}
	return cities[0].name
}

var asciiFold = strings.NewReplacer(
	"ç", "c", "Ç", "c", "ğ", "g", "Ğ", "g", "ı", "i", "İ", "i",
	"ö", "o", "Ö", "o", "ş", "s", "Ş", "s", "ü", "u", "Ü", "u",
)

func uniqueEmail(rng *rand.Rand, used map[string]int, first, last string) string {
	local := strings.ToLower(asciiFold.Replace(first + "." + last))
	domain := emailDomains[rng.IntN(len(emailDomains))]
	key := local + "@" + domain
	used[key]++
	if n := used[key]; n > 1 {
		return fmt.Sprintf("%s%d@%s", local, n, domain)
	}
	return key
}

// phone returns a Turkish mobile number in E.164: +90 5XX XXX XX XX.
func phone(rng *rand.Rand) string {
	return fmt.Sprintf("+905%d%s", 30+rng.IntN(30), digits(rng, 7))
}

func digits(rng *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteByte(byte('0' + rng.IntN(10)))
	}
	return b.String()
}

func clampAfter(t, min time.Time) *time.Time {
	t = t.Truncate(time.Second)
	if t.Before(min) {
		t = min
	}
	return &t
}
