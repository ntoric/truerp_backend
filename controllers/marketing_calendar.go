package controllers

// Marketing → Calendar: Indian and international festivals, national days
// and shopping days, plus the store's campaigns on the same calendar, and the
// recurrence rules for repeating campaigns.
//
// Occasion dates come from three kinds of rule:
//   - fixed (Independence Day, Christmas…), any year;
//   - computed (Mother's Day = 2nd Sunday of May, Black Friday, Easter…),
//     any year;
//   - per-year tables for lunar festivals (Diwali, Holi, Eid…). These follow
//     published Indian calendars; Islamic dates depend on the moon sighting
//     and can shift by a day. Years outside the table have no date, so a
//     yearly-on-the-occasion series ends when the table runs out.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	mktStatusCompleted = "completed"

	mktRecurNone           = ""
	mktRecurYearlyOccasion = "yearly_occasion"
	mktRecurYearly         = "yearly"
	mktRecurMonthly        = "monthly"
	mktRecurWeekly         = "weekly"

	// A run more than this late (server was offline) is skipped rather than
	// sent: a festival greeting days after the festival does more harm than good.
	mktMaxRunDelay = 24 * time.Hour

	mktCalendarMaxDays = 400
	ymdLayout          = "2006-01-02"
)

var marketingRecurrences = map[string]bool{
	mktRecurNone: true, mktRecurYearlyOccasion: true, mktRecurYearly: true,
	mktRecurMonthly: true, mktRecurWeekly: true,
}

// ---------------------------------------------------------------------------
// Occasion catalogue
// ---------------------------------------------------------------------------

type occasionRule struct {
	// Fixed date.
	Month, Day int
	// Nth weekday of Month (Nth -1 = last), then OffsetDays.
	Weekday    time.Weekday
	Nth        int
	OffsetDays int
	// Days from Easter Sunday.
	Easter       bool
	EasterOffset int
	// Per-year "MM-DD" for lunar festivals.
	Dates map[int]string
}

type marketingOccasion struct {
	Key          string
	Name         string
	Region       string // india, international
	Category     string // festival, national, shopping, celebration, awareness
	Emoji        string
	Description  string
	CampaignType string
	Rule         occasionRule
}

func fixedDay(m, d int) occasionRule { return occasionRule{Month: m, Day: d} }
func nthWeekday(m int, wd time.Weekday, nth, offset int) occasionRule {
	return occasionRule{Month: m, Weekday: wd, Nth: nth, OffsetDays: offset}
}
func easterDay(offset int) occasionRule   { return occasionRule{Easter: true, EasterOffset: offset} }
func lunar(d map[int]string) occasionRule { return occasionRule{Dates: d} }

var marketingOccasions = []marketingOccasion{
	// --- Indian festivals ---------------------------------------------------
	{Key: "lohri", Name: "Lohri", Region: "india", Category: "festival", Emoji: "🔥", Description: "Punjabi harvest festival", Rule: lunar(map[int]string{2026: "01-13", 2027: "01-14", 2028: "01-14"})},
	{Key: "makar_sankranti", Name: "Makar Sankranti / Pongal", Region: "india", Category: "festival", Emoji: "🪁", Description: "Harvest festival (Pongal, Uttarayan, Bihu)", Rule: lunar(map[int]string{2026: "01-14", 2027: "01-15", 2028: "01-15"})},
	{Key: "vasant_panchami", Name: "Vasant Panchami", Region: "india", Category: "festival", Emoji: "🌼", Description: "Saraswati Puja, start of spring", Rule: lunar(map[int]string{2026: "01-23", 2027: "02-11", 2028: "01-31"})},
	{Key: "maha_shivaratri", Name: "Maha Shivaratri", Region: "india", Category: "festival", Emoji: "🔱", Description: "Night of Lord Shiva", Rule: lunar(map[int]string{2026: "02-15", 2027: "03-06", 2028: "02-23"})},
	{Key: "holi", Name: "Holi", Region: "india", Category: "festival", Emoji: "🎨", Description: "Festival of colours", Rule: lunar(map[int]string{2026: "03-04", 2027: "03-22", 2028: "03-11"})},
	{Key: "ugadi", Name: "Ugadi / Gudi Padwa", Region: "india", Category: "festival", Emoji: "🌿", Description: "New Year in Karnataka, Andhra, Telangana and Maharashtra", Rule: lunar(map[int]string{2026: "03-19", 2027: "04-07", 2028: "03-27"})},
	{Key: "ram_navami", Name: "Ram Navami", Region: "india", Category: "festival", Emoji: "🏹", Description: "Birth of Lord Rama", Rule: lunar(map[int]string{2026: "03-26", 2027: "04-15", 2028: "04-03"})},
	{Key: "baisakhi", Name: "Baisakhi / Vishu / Puthandu", Region: "india", Category: "festival", Emoji: "🌾", Description: "Harvest and regional New Year (Punjab, Kerala, Tamil Nadu, Bengal)", Rule: lunar(map[int]string{2026: "04-14", 2027: "04-14", 2028: "04-13"})},
	{Key: "akshaya_tritiya", Name: "Akshaya Tritiya", Region: "india", Category: "festival", Emoji: "🪙", Description: "Auspicious day for buying gold and new beginnings", Rule: lunar(map[int]string{2026: "04-19", 2027: "05-09", 2028: "04-27"})},
	{Key: "buddha_purnima", Name: "Buddha Purnima", Region: "india", Category: "festival", Emoji: "☸️", Description: "Birth of Gautama Buddha", Rule: lunar(map[int]string{2026: "05-01", 2027: "05-20", 2028: "05-08"})},
	{Key: "raksha_bandhan", Name: "Raksha Bandhan", Region: "india", Category: "festival", Emoji: "🪢", Description: "Celebrating the bond of brothers and sisters", Rule: lunar(map[int]string{2026: "08-28", 2027: "08-17", 2028: "08-05"})},
	{Key: "onam", Name: "Onam", Region: "india", Category: "festival", Emoji: "🌸", Description: "Kerala harvest festival (Thiruvonam)", Rule: lunar(map[int]string{2026: "08-26", 2027: "09-12", 2028: "08-31"})},
	{Key: "janmashtami", Name: "Janmashtami", Region: "india", Category: "festival", Emoji: "🦚", Description: "Birth of Lord Krishna", Rule: lunar(map[int]string{2026: "09-04", 2027: "08-25", 2028: "08-13"})},
	{Key: "ganesh_chaturthi", Name: "Ganesh Chaturthi", Region: "india", Category: "festival", Emoji: "🐘", Description: "Birth of Lord Ganesha", Rule: lunar(map[int]string{2026: "09-14", 2027: "09-04", 2028: "08-23"})},
	{Key: "navratri", Name: "Navratri", Region: "india", Category: "festival", Emoji: "🪔", Description: "First of nine nights of Goddess Durga, Garba and Durga Puja season", Rule: lunar(map[int]string{2026: "10-11", 2027: "09-30", 2028: "09-19"})},
	{Key: "dussehra", Name: "Dussehra", Region: "india", Category: "festival", Emoji: "🏹", Description: "Vijayadashami, victory of good over evil", Rule: lunar(map[int]string{2026: "10-20", 2027: "10-09", 2028: "09-27"})},
	{Key: "karwa_chauth", Name: "Karwa Chauth", Region: "india", Category: "festival", Emoji: "🌕", Description: "Fast observed for spouses", Rule: lunar(map[int]string{2026: "10-29", 2027: "10-18", 2028: "10-06"})},
	{Key: "dhanteras", Name: "Dhanteras", Region: "india", Category: "festival", Emoji: "🪙", Description: "First day of Diwali, auspicious for buying gold, utensils and electronics", Rule: lunar(map[int]string{2026: "11-06", 2027: "10-27", 2028: "10-15"})},
	{Key: "diwali", Name: "Diwali", Region: "india", Category: "festival", Emoji: "🪔", Description: "Festival of lights, the biggest shopping season of the year", Rule: lunar(map[int]string{2026: "11-08", 2027: "10-29", 2028: "10-17"})},
	{Key: "bhai_dooj", Name: "Bhai Dooj", Region: "india", Category: "festival", Emoji: "🎁", Description: "Celebrating brothers and sisters, end of Diwali", Rule: lunar(map[int]string{2026: "11-11", 2027: "10-31", 2028: "10-19"})},
	{Key: "guru_nanak_jayanti", Name: "Guru Nanak Jayanti", Region: "india", Category: "festival", Emoji: "🙏", Description: "Gurpurab, birth of Guru Nanak Dev Ji", Rule: lunar(map[int]string{2026: "11-24", 2027: "11-14", 2028: "11-02"})},
	{Key: "ramadan", Name: "Ramadan", Region: "india", Category: "festival", Emoji: "🌙", Description: "First day of the holy month of fasting (moon sighting may shift it by a day)", Rule: lunar(map[int]string{2026: "02-19", 2027: "02-08", 2028: "01-28"})},
	{Key: "eid_ul_fitr", Name: "Eid al-Fitr", Region: "india", Category: "festival", Emoji: "🌙", Description: "End of Ramadan (moon sighting may shift it by a day)", Rule: lunar(map[int]string{2026: "03-21", 2027: "03-10", 2028: "02-27"})},
	{Key: "eid_ul_adha", Name: "Eid al-Adha (Bakrid)", Region: "india", Category: "festival", Emoji: "🕌", Description: "Festival of sacrifice (moon sighting may shift it by a day)", Rule: lunar(map[int]string{2026: "05-27", 2027: "05-17", 2028: "05-05"})},

	// --- Indian national & observance days ----------------------------------
	{Key: "national_youth_day", Name: "National Youth Day", Region: "india", Category: "awareness", Emoji: "🧑‍🎓", Description: "Swami Vivekananda Jayanti", Rule: fixedDay(1, 12)},
	{Key: "republic_day", Name: "Republic Day", Region: "india", Category: "national", Emoji: "🇮🇳", Description: "Republic Day sales and greetings", Rule: fixedDay(1, 26)},
	{Key: "doctors_day", Name: "National Doctors' Day", Region: "india", Category: "awareness", Emoji: "🩺", Description: "Thanking doctors", Rule: fixedDay(7, 1)},
	{Key: "friendship_day", Name: "Friendship Day", Region: "india", Category: "celebration", Emoji: "🤝", Description: "First Sunday of August in India", Rule: nthWeekday(8, time.Sunday, 1, 0)},
	{Key: "independence_day", Name: "Independence Day", Region: "india", Category: "national", Emoji: "🇮🇳", Description: "Freedom sale and greetings", Rule: fixedDay(8, 15)},
	{Key: "teachers_day", Name: "Teachers' Day", Region: "india", Category: "celebration", Emoji: "📚", Description: "Dr. Sarvepalli Radhakrishnan's birthday", Rule: fixedDay(9, 5)},
	{Key: "gandhi_jayanti", Name: "Gandhi Jayanti", Region: "india", Category: "national", Emoji: "🕊️", Description: "Birth anniversary of Mahatma Gandhi", Rule: fixedDay(10, 2)},
	{Key: "childrens_day", Name: "Children's Day", Region: "india", Category: "celebration", Emoji: "🧒", Description: "Pandit Nehru's birthday", Rule: fixedDay(11, 14)},

	// --- International ------------------------------------------------------
	{Key: "new_year", Name: "New Year's Day", Region: "international", Category: "celebration", Emoji: "🎆", Description: "New Year greetings and offers", Rule: fixedDay(1, 1)},
	{Key: "chinese_new_year", Name: "Lunar New Year", Region: "international", Category: "festival", Emoji: "🧧", Description: "Chinese New Year", Rule: lunar(map[int]string{2026: "02-17", 2027: "02-06", 2028: "01-26"})},
	{Key: "valentines_day", Name: "Valentine's Day", Region: "international", Category: "celebration", Emoji: "❤️", Description: "Gifts for loved ones", Rule: fixedDay(2, 14)},
	{Key: "womens_day", Name: "International Women's Day", Region: "international", Category: "celebration", Emoji: "👩", Description: "Celebrating women", Rule: fixedDay(3, 8)},
	{Key: "consumer_rights_day", Name: "World Consumer Rights Day", Region: "international", Category: "awareness", Emoji: "🛒", Description: "Thank your customers", Rule: fixedDay(3, 15)},
	{Key: "april_fools", Name: "April Fools' Day", Region: "international", Category: "celebration", Emoji: "🤡", Description: "Fun, playful offers", Rule: fixedDay(4, 1)},
	{Key: "easter", Name: "Easter", Region: "international", Category: "festival", Emoji: "🐣", Description: "Easter Sunday", Rule: easterDay(0)},
	{Key: "world_health_day", Name: "World Health Day", Region: "international", Category: "awareness", Emoji: "💪", Description: "Health and wellness offers", Rule: fixedDay(4, 7)},
	{Key: "earth_day", Name: "Earth Day", Region: "international", Category: "awareness", Emoji: "🌍", Description: "Eco-friendly products and green offers", Rule: fixedDay(4, 22)},
	{Key: "labour_day", Name: "Labour Day", Region: "international", Category: "national", Emoji: "🛠️", Description: "May Day (also Maharashtra & Gujarat Day)", Rule: fixedDay(5, 1)},
	{Key: "mothers_day", Name: "Mother's Day", Region: "international", Category: "celebration", Emoji: "💐", Description: "Second Sunday of May", Rule: nthWeekday(5, time.Sunday, 2, 0)},
	{Key: "environment_day", Name: "World Environment Day", Region: "international", Category: "awareness", Emoji: "🌱", Description: "Go green", Rule: fixedDay(6, 5)},
	{Key: "fathers_day", Name: "Father's Day", Region: "international", Category: "celebration", Emoji: "👔", Description: "Third Sunday of June", Rule: nthWeekday(6, time.Sunday, 3, 0)},
	{Key: "yoga_day", Name: "International Yoga Day", Region: "international", Category: "awareness", Emoji: "🧘", Description: "Health, fitness and wellness", Rule: fixedDay(6, 21)},
	{Key: "halloween", Name: "Halloween", Region: "international", Category: "celebration", Emoji: "🎃", Description: "Spooky offers", Rule: fixedDay(10, 31)},
	{Key: "singles_day", Name: "Singles' Day (11.11)", Region: "international", Category: "shopping", Emoji: "🛍️", Description: "The world's biggest online shopping day", Rule: fixedDay(11, 11)},
	{Key: "mens_day", Name: "International Men's Day", Region: "international", Category: "celebration", Emoji: "🧔", Description: "Celebrating men", Rule: fixedDay(11, 19)},
	{Key: "thanksgiving", Name: "Thanksgiving", Region: "international", Category: "celebration", Emoji: "🦃", Description: "Fourth Thursday of November (US)", Rule: nthWeekday(11, time.Thursday, 4, 0)},
	{Key: "black_friday", Name: "Black Friday", Region: "international", Category: "shopping", Emoji: "🏷️", Description: "Biggest sale day of the year, day after Thanksgiving", Rule: nthWeekday(11, time.Thursday, 4, 1)},
	{Key: "cyber_monday", Name: "Cyber Monday", Region: "international", Category: "shopping", Emoji: "💻", Description: "Online deals, Monday after Thanksgiving", Rule: nthWeekday(11, time.Thursday, 4, 4)},
	{Key: "christmas_eve", Name: "Christmas Eve", Region: "international", Category: "festival", Emoji: "🎄", Description: "Last-minute gifting", Rule: fixedDay(12, 24)},
	{Key: "christmas", Name: "Christmas", Region: "international", Category: "festival", Emoji: "🎅", Description: "Season's greetings and gifting", Rule: fixedDay(12, 25)},
	{Key: "boxing_day", Name: "Boxing Day", Region: "international", Category: "shopping", Emoji: "📦", Description: "Post-Christmas sales", Rule: fixedDay(12, 26)},
	{Key: "new_years_eve", Name: "New Year's Eve", Region: "international", Category: "celebration", Emoji: "🥂", Description: "Year-end party offers", Rule: fixedDay(12, 31)},
}

var marketingOccasionByKey = func() map[string]*marketingOccasion {
	m := make(map[string]*marketingOccasion, len(marketingOccasions))
	for i := range marketingOccasions {
		o := &marketingOccasions[i]
		if o.CampaignType == "" {
			switch o.Category {
			case "festival":
				o.CampaignType = "festival"
			case "national", "shopping":
				o.CampaignType = "offer"
			case "awareness":
				o.CampaignType = "announcement"
			default:
				o.CampaignType = "promotion"
			}
		}
		m[o.Key] = o
	}
	return m
}()

// marketingLunarYears is the year range covered by the lunar tables.
func marketingLunarYears() (int, int) {
	lo, hi := 0, 0
	for _, o := range marketingOccasions {
		for y := range o.Rule.Dates {
			if lo == 0 || y < lo {
				lo = y
			}
			if y > hi {
				hi = y
			}
		}
	}
	return lo, hi
}

// easterSunday returns Easter Sunday (Gregorian) for a year (Meeus/Jones/Butcher).
func easterSunday(year int) time.Time {
	a := year % 19
	b, c := year/100, year%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// date returns the occasion's date in a year (UTC midnight, a plain date).
func (o *marketingOccasion) date(year int) (time.Time, bool) {
	r := o.Rule
	switch {
	case r.Dates != nil:
		md, ok := r.Dates[year]
		if !ok {
			return time.Time{}, false
		}
		t, err := time.Parse(ymdLayout, fmt.Sprintf("%04d-%s", year, md))
		return t, err == nil
	case r.Easter:
		return easterSunday(year).AddDate(0, 0, r.EasterOffset), true
	case r.Nth != 0:
		var t time.Time
		if r.Nth > 0 {
			t = time.Date(year, time.Month(r.Month), 1, 0, 0, 0, 0, time.UTC)
			t = t.AddDate(0, 0, (int(r.Weekday)-int(t.Weekday())+7)%7+7*(r.Nth-1))
		} else {
			t = time.Date(year, time.Month(r.Month)+1, 0, 0, 0, 0, 0, time.UTC)
			t = t.AddDate(0, 0, -((int(t.Weekday()) - int(r.Weekday) + 7) % 7))
		}
		return t.AddDate(0, 0, r.OffsetDays), true
	default:
		return time.Date(year, time.Month(r.Month), r.Day, 0, 0, 0, 0, time.UTC), true
	}
}

type marketingOccasionDTO struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Date         string `json:"date"` // YYYY-MM-DD
	Region       string `json:"region"`
	Category     string `json:"category"`
	Emoji        string `json:"emoji"`
	Description  string `json:"description"`
	CampaignType string `json:"campaign_type"`
	// Lunar: the date moves every year (and is only known for covered years).
	Lunar bool `json:"lunar"`
}

// occasionsBetween lists occasions dated from..to inclusive (plain dates),
// sorted by date then name; region and key filter when non-empty.
func occasionsBetween(from, to time.Time, region, key string) []marketingOccasionDTO {
	out := []marketingOccasionDTO{}
	for i := range marketingOccasions {
		o := &marketingOccasions[i]
		if (region != "" && o.Region != region) || (key != "" && o.Key != key) {
			continue
		}
		for y := from.Year(); y <= to.Year(); y++ {
			d, ok := o.date(y)
			if !ok || d.Before(from) || d.After(to) {
				continue
			}
			out = append(out, marketingOccasionDTO{
				Key: o.Key, Name: o.Name, Date: d.Format(ymdLayout), Region: o.Region,
				Category: o.Category, Emoji: o.Emoji, Description: o.Description,
				CampaignType: o.CampaignType, Lunar: o.Rule.Dates != nil,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// nextOccasionDate is the occasion's first date strictly after `after`
// (looks at most two years ahead).
func nextOccasionDate(key string, after time.Time) (time.Time, bool) {
	o := marketingOccasionByKey[key]
	if o == nil {
		return time.Time{}, false
	}
	for y := after.Year(); y <= after.Year()+2; y++ {
		if d, ok := o.date(y); ok && d.After(after) {
			return d, true
		}
	}
	return time.Time{}, false
}

// ---------------------------------------------------------------------------
// Recurrence
// ---------------------------------------------------------------------------

// addMonthsClamped adds months keeping the day of month, clamped to the
// target month's last day (Jan 31 + 1 month = Feb 28/29, + 2 = Mar 31).
func addMonthsClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

// localYMD is the calendar date of t in server-local time.
func localYMD(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// marketingRun is one scheduled send of a series.
type marketingRun struct {
	At           time.Time
	OccasionDate string
}

// nextMarketingRun returns run number n (1 = the run after SeriesStart) of a
// series, given the previous run, or false when the series has ended (past
// RecurrenceUntil, or the occasion's date isn't known for that year).
func nextMarketingRun(camp *models.MarketingCampaign, prev marketingRun, n int) (marketingRun, bool) {
	start := prev.At
	if camp.SeriesStart != nil {
		start = camp.SeriesStart.In(time.Local)
	}
	var next marketingRun
	switch camp.Recurrence {
	case mktRecurWeekly:
		next.At = start.AddDate(0, 0, 7*n)
	case mktRecurMonthly:
		next.At = addMonthsClamped(start, n)
	case mktRecurYearly:
		next.At = addMonthsClamped(start, 12*n)
	case mktRecurYearlyOccasion:
		cur, err := time.Parse(ymdLayout, prev.OccasionDate)
		if err != nil {
			return next, false
		}
		nd, ok := nextOccasionDate(camp.OccasionKey, cur)
		if !ok {
			return next, false
		}
		// Keep the run's offset from the occasion (e.g. the day before).
		next.At = prev.At.In(time.Local).AddDate(0, 0, int(nd.Sub(cur).Hours()/24))
		next.OccasionDate = nd.Format(ymdLayout)
	default:
		return next, false
	}
	if camp.Recurrence != mktRecurYearlyOccasion && prev.OccasionDate != "" {
		// Keep the occasion tag only while the shifted date is still the
		// occasion (Christmas yearly: yes; Diwali on a fixed date: no).
		if od, err := time.Parse(ymdLayout, prev.OccasionDate); err == nil {
			shifted := od.AddDate(0, 0, int(localYMD(next.At).Sub(localYMD(prev.At)).Hours()/24))
			if o := marketingOccasionByKey[camp.OccasionKey]; o != nil {
				if d, ok := o.date(shifted.Year()); ok && d.Equal(shifted) {
					next.OccasionDate = shifted.Format(ymdLayout)
				}
			}
		}
	}
	if until, err := time.Parse(ymdLayout, camp.RecurrenceUntil); err == nil && localYMD(next.At).After(until) {
		return next, false
	}
	return next, true
}

// recurrenceLabel describes a series, e.g. "Every year on Diwali".
func recurrenceLabel(camp *models.MarketingCampaign) string {
	switch camp.Recurrence {
	case mktRecurYearlyOccasion:
		return "Every year on " + camp.OccasionName
	case mktRecurYearly:
		return "Every year"
	case mktRecurMonthly:
		return "Every month"
	case mktRecurWeekly:
		return "Every week"
	}
	return ""
}

// validateMarketingRecurrence checks the occasion / recurrence fields once
// they are copied onto camp (called from apply).
func validateMarketingRecurrence(camp *models.MarketingCampaign) error {
	if camp.OccasionKey != "" {
		o := marketingOccasionByKey[camp.OccasionKey]
		if o == nil {
			return fmt.Errorf("unknown occasion %q", camp.OccasionKey)
		}
		camp.OccasionName = o.Name
		if camp.OccasionDate != "" {
			d, err := time.Parse(ymdLayout, camp.OccasionDate)
			if err != nil {
				return errors.New("Occasion date must be a date (YYYY-MM-DD)")
			}
			if od, ok := o.date(d.Year()); !ok || !od.Equal(d) {
				return fmt.Errorf("%s is not on %s", o.Name, formatMarketingDate(camp.OccasionDate))
			}
		}
	} else {
		camp.OccasionName, camp.OccasionDate = "", ""
	}
	if !marketingRecurrences[camp.Recurrence] {
		return fmt.Errorf("unknown repeat option %q", camp.Recurrence)
	}
	if camp.Recurrence == mktRecurYearlyOccasion && (camp.OccasionKey == "" || camp.OccasionDate == "") {
		return errors.New("Pick an occasion from the calendar to repeat on it every year")
	}
	if camp.RecurrenceUntil != "" {
		if camp.Recurrence == mktRecurNone {
			camp.RecurrenceUntil = ""
		} else if _, err := time.Parse(ymdLayout, camp.RecurrenceUntil); err != nil {
			return errors.New("Repeat until must be a date (YYYY-MM-DD)")
		}
	}
	return nil
}

// startMarketingSeries sets the series fields when a campaign is scheduled
// (or clears them when it isn't).
func startMarketingSeries(camp *models.MarketingCampaign, at *time.Time) error {
	camp.RunCount = 0
	camp.SeriesStart = nil
	if at == nil || camp.Recurrence == mktRecurNone {
		return nil
	}
	if until, err := time.Parse(ymdLayout, camp.RecurrenceUntil); err == nil && localYMD(*at).After(until) {
		return errors.New("Repeat until must be on or after the first send date")
	}
	start := *at
	camp.SeriesStart = &start
	return nil
}

// spawnMarketingRun copies a series into a one-off child campaign for one run
// and returns it in "sending" status (ready for runMarketingCampaign).
func spawnMarketingRun(series *models.MarketingCampaign, at time.Time, occasionDate string) (models.MarketingCampaign, error) {
	run := *series
	run.ID = uuid.New()
	parent := series.ID
	run.ParentID = &parent
	label := occasionDate
	if label == "" {
		label = localYMD(at).Format(ymdLayout)
	}
	run.Name = series.Name + " · " + formatMarketingDate(label)
	run.OccasionDate = occasionDate
	run.Recurrence, run.RecurrenceUntil, run.SeriesStart, run.RunCount = mktRecurNone, "", nil, 0
	run.Status = mktStatusSending
	t := at
	run.ScheduledAt = &t
	run.SentAt, run.LastError = nil, ""
	run.TotalRecipients, run.SentCount, run.FailedCount, run.SkippedCount = 0, 0, 0, 0
	run.CreatedAt, run.UpdatedAt = time.Time{}, time.Time{}
	err := utils.DB.Create(&run).Error
	return run, err
}

// fireMarketingSeries handles a due recurring series: advances it to its next
// run (or completes it), then sends the due run as a child campaign. The
// run_count guard makes the advance atomic, so a run is sent once.
func fireMarketingSeries(id uuid.UUID) {
	var series models.MarketingCampaign
	if err := utils.DB.First(&series, "id = ? AND status = ?", id, mktStatusScheduled).Error; err != nil || series.ScheduledAt == nil {
		return
	}
	now := time.Now()
	due := marketingRun{At: series.ScheduledAt.In(time.Local), OccasionDate: series.OccasionDate}

	// Next run in the future (runs missed while offline are skipped).
	n := series.RunCount + 1
	next, ok := nextMarketingRun(&series, due, n)
	for ok && !next.At.After(now) {
		n++
		next, ok = nextMarketingRun(&series, next, n)
	}

	updates := map[string]interface{}{"run_count": n, "last_error": ""}
	if ok {
		updates["scheduled_at"] = next.At
		updates["occasion_date"] = next.OccasionDate
	} else {
		updates["status"] = mktStatusCompleted
	}
	late := now.Sub(due.At) > mktMaxRunDelay
	if late {
		updates["last_error"] = fmt.Sprintf("Run of %s was skipped: it was more than a day late (server offline)", due.At.Format("02 Jan 2006 15:04"))
	}
	res := utils.DB.Model(&models.MarketingCampaign{}).
		Where("id = ? AND status = ? AND run_count = ?", series.ID, mktStatusScheduled, series.RunCount).
		Updates(updates)
	if res.Error != nil || res.RowsAffected != 1 {
		return
	}
	if late {
		log.Printf("[marketing] series %s (%s): skipped late run of %s", series.ID, series.Name, due.At)
		return
	}
	run, err := spawnMarketingRun(&series, due.At, due.OccasionDate)
	if err != nil {
		log.Printf("[marketing] series %s: failed to create run: %v", series.ID, err)
		return
	}
	runMarketingCampaign(run.ID)
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// parseCalendarRange reads ?from=&to= (YYYY-MM-DD, inclusive), defaulting to
// the current month.
func parseCalendarRange(c *gin.Context) (time.Time, time.Time, error) {
	today := localYMD(time.Now())
	from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	if s := c.Query("from"); s != "" {
		t, err := time.Parse(ymdLayout, s)
		if err != nil {
			return from, to, errors.New("from must be YYYY-MM-DD")
		}
		from = t
	}
	if s := c.Query("to"); s != "" {
		t, err := time.Parse(ymdLayout, s)
		if err != nil {
			return from, to, errors.New("to must be YYYY-MM-DD")
		}
		to = t
	}
	if to.Before(from) {
		return from, to, errors.New("to must be on or after from")
	}
	if to.Sub(from) > mktCalendarMaxDays*24*time.Hour {
		return from, to, fmt.Errorf("pick a range of at most %d days", mktCalendarMaxDays)
	}
	return from, to, nil
}

func normalizeRegion(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "india" || s == "international" {
		return s
	}
	return ""
}

// GetMarketingOccasions lists occasions in a date range (?from&to, optional
// ?region=india|international and ?key=).
func GetMarketingOccasions(c *gin.Context) {
	from, to, err := parseCalendarRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, occasionsBetween(from, to, normalizeRegion(c.Query("region")), strings.TrimSpace(c.Query("key"))))
}

type marketingCalendarEvent struct {
	CampaignID   uuid.UUID  `json:"campaign_id"`
	SeriesID     *uuid.UUID `json:"series_id,omitempty"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	At           time.Time  `json:"at"`
	Projected    bool       `json:"projected"` // a future run of a series, not created yet
	Recurrence   string     `json:"recurrence,omitempty"`
	Repeat       string     `json:"repeat,omitempty"`
	OccasionName string     `json:"occasion_name,omitempty"`
	Channels     string     `json:"channels"`
}

// GetMarketingCalendar returns the occasions and the store's campaigns
// (scheduled, sent, and projected future runs of repeating campaigns) in a
// date range.
func GetMarketingCalendar(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	from, to, err := parseCalendarRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Event times are compared as instants; pad a day each side so every
	// viewer timezone sees its whole range, the client places them by date.
	lo := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local).Add(-24 * time.Hour)
	hi := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.Local).Add(48 * time.Hour)

	var camps []models.MarketingCampaign
	if err := utils.DB.Where("user_id = ?", userID).
		Where("(scheduled_at >= ? AND scheduled_at < ?) OR (sent_at >= ? AND sent_at < ?) OR (status = ? AND recurrence <> '')",
			lo, hi, lo, hi, mktStatusScheduled).
		Find(&camps).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load campaigns"})
		return
	}

	events := []marketingCalendarEvent{}
	in := func(t time.Time) bool { return !t.Before(lo) && t.Before(hi) }
	for i := range camps {
		camp := &camps[i]
		ev := marketingCalendarEvent{
			CampaignID: camp.ID, SeriesID: camp.ParentID, Name: camp.Name, Status: camp.Status,
			Recurrence: camp.Recurrence, Repeat: recurrenceLabel(camp), OccasionName: camp.OccasionName, Channels: camp.Channels,
		}
		switch {
		case camp.SentAt != nil && camp.Recurrence == mktRecurNone:
			if in(*camp.SentAt) {
				ev.At = *camp.SentAt
				events = append(events, ev)
			}
		case camp.Status == mktStatusScheduled && camp.ScheduledAt != nil:
			run := marketingRun{At: camp.ScheduledAt.In(time.Local), OccasionDate: camp.OccasionDate}
			if in(run.At) {
				ev.At = run.At
				events = append(events, ev)
			}
			if camp.Recurrence == mktRecurNone {
				continue
			}
			for n, k := camp.RunCount+1, 0; k < 400; n, k = n+1, k+1 {
				next, ok := nextMarketingRun(camp, run, n)
				if !ok || !next.At.Before(hi) {
					break
				}
				run = next
				if in(run.At) {
					p := ev
					p.At, p.Projected = run.At, true
					events = append(events, p)
				}
			}
		case camp.ScheduledAt != nil && camp.Status != mktStatusCompleted:
			// draft with a past schedule, sending, failed without sent_at…
			if in(*camp.ScheduledAt) && camp.Status != mktStatusDraft {
				ev.At = *camp.ScheduledAt
				events = append(events, ev)
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })

	lunarFrom, lunarTo := marketingLunarYears()
	c.JSON(http.StatusOK, gin.H{
		"from": from.Format(ymdLayout), "to": to.Format(ymdLayout),
		"occasions":   occasionsBetween(from, to, normalizeRegion(c.Query("region")), ""),
		"events":      events,
		"lunar_years": gin.H{"from": lunarFrom, "to": lunarTo},
	})
}
