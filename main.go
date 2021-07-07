package main

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v71/invoiceitem"

	"github.com/stripe/stripe-go/v71/invoice"

	"github.com/stripe/stripe-go/v71"

	"github.com/dnoberon/charger/airtable"

	"github.com/joho/godotenv"
)

type InvoiceItem struct {
	item             string
	quantity         int64
	amount           float64
	currencyCode     string
	property         string
	airtableRecordID string
	dateServiced     time.Time
}

// needed to sort the invoices by time serviced
type invoiceByTime []InvoiceItem

func (p invoiceByTime) Len() int {
	return len(p)
}

func (p invoiceByTime) Less(i, j int) bool {
	return p[i].dateServiced.Before(p[j].dateServiced)
}

func (p invoiceByTime) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func main() {
	var staleDays int

	godotenv.Load()

	airtableAPIKey := os.Getenv("AIRTABLE_API_KEY")
	baseID := os.Getenv("AIRTABLE_BASE_ID")
	tableName := os.Getenv("TABLENAME")

	stripeCustomerIDColumn := os.Getenv("STRIPE_CUSTOMER_ID_COLUMN")
	invoiceAmountColumn := os.Getenv("INVOICE_AMOUNT_COLUMN")
	paidColumn := os.Getenv("PAID_COLUMN")
	notesColumn := os.Getenv("NOTES_COLUMN")
	currencyCodeColumn := os.Getenv("CURRENCY_CODE_COLUMN")
	dateColumn := os.Getenv("DATE_COLUMN")
	quantityColumn := os.Getenv("QUANTITY_COLUMN")
	itemColumn := os.Getenv("ITEM_COLUMN")
	propertyColumn := os.Getenv("PROPERTY_COLUMN")
	dateServicedColumn := os.Getenv("DATE_SERVICED_COLUMN")

	staleDays, err := strconv.Atoi(os.Getenv("STALE_DAYS"))
	if err != nil {
		staleDays = -7
	}

	airtableClient, err := airtable.NewAirtableClient(airtableAPIKey, baseID)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Charger Running....")

	// start process loop
	for {
		offset := ""
		records := []airtable.Record{}

		// inner loop handles constantly fetching from airtable until we have all the records
		for {
			results, err := fetchAirtableRecords(offset)
			if err != nil {
				log.Printf("error fetching airtable records %v", err)
				break
			}

			records = append(records, results.Records...)

			// if we don't have an offset parameter, then we have fetched all records
			if results.Offset == "" {
				break
			}

			offset = results.Offset
		}

		// each record represents a possible invoice item, key is the stripe customer ID - so we charge them all
		// at the same time to the right people
		customerInvoices := map[string][]InvoiceItem{}
		for _, record := range records {
			// verify that if we have a value in the paid column, it's not true or false
			val, ok := record.Fields[paidColumn]
			if ok {
				if fmt.Sprintf("%v", val) == "true" {
					log.Printf("customer already charged for this record, skipping")
					continue
				}
			}

			invoice := InvoiceItem{}
			// check to see if theres a date, bill only on or after said date
			loc, err := time.LoadLocation(os.Getenv("TIMEZONE"))
			if err != nil {
				log.Fatal("incorrect time location!")
				continue
			}

			val, ok = record.Fields[dateColumn]
			if ok {
				date, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%v", val), loc)
				if err == nil {
					// if we're not on or after date, skip this record
					if !time.Now().In(loc).After(date) {
						log.Printf("date in future, skipping")
						continue
					}

					past := time.Now().In(loc).AddDate(0, 0, staleDays)

					if date.Before(past) {
						log.Printf("pay date too old, skipping")
						continue
					}
				}

			} else if !ok {
				log.Printf("date not present, skipping")
				continue
			}

			val, ok = record.Fields[dateServicedColumn]
			if ok {
				serviceDate, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%v", val), loc)
				if err != nil {
					log.Printf("error parsing service date")
					continue
				}

				invoice.dateServiced = serviceDate

			} else if !ok {
				log.Printf("service date not present, skipping")
				continue
			}

			// we need to handle rollup fields here, so run reflect and extract if slice
			var customerID string
			val, ok = record.Fields[stripeCustomerIDColumn]
			if !ok {
				log.Printf("customerID not present, skipping")
				continue
			}

			// we're still making some assumptions here - like it's a slice of strings and not ints
			rt := reflect.TypeOf(val)
			switch rt.Kind() {
			case reflect.Slice:
				c := val.([]interface{})
				if len(c) > 0 {
					customerID = fmt.Sprintf("%v", c[0])
				}
			case reflect.Array:
				c := val.([]interface{})
				if len(c) > 0 {
					customerID = fmt.Sprintf("%v", c[0])
				}
			case reflect.String:
				customerID = fmt.Sprintf("%s", val)
			}

			val, ok = record.Fields[currencyCodeColumn]
			if !ok {
				log.Printf("currency code not present, skipping")
				continue
			}

			invoice.currencyCode = strings.ToLower(fmt.Sprintf("%s", val))

			// invoiceAmount must be a float64
			invoiceAmount, ok := record.Fields[invoiceAmountColumn]
			if !ok {
				log.Printf("invoice amount not present, skipping")
				continue
			}

			if invoiceAmount.(float64) == 0 {
				log.Printf("invoice amount not greater than 0, skipping")
				continue
			}

			invoice.amount = invoiceAmount.(float64)

			quantity, ok := record.Fields[quantityColumn]
			if !ok {
				log.Printf("quantity not present, skipping")
				continue
			}

			invoice.quantity = int64(quantity.(float64))

			item, ok := record.Fields[itemColumn]
			if !ok {
				log.Printf("item not present, skipping")
				continue
			}

			invoice.item = fmt.Sprintf("%v", item)

			propertyName, ok := record.Fields[propertyColumn]
			if !ok {
				log.Printf("property not present, skipping")
				continue
			}

			invoice.property = fmt.Sprintf("%v", propertyName)

			invoice.airtableRecordID = record.ID
			customerInvoices[customerID] = append(customerInvoices[customerID], invoice)
		}

		// for each customer create a chargeStripe for all invoices sharing the same currency
		for customerID, invoices := range customerInvoices {
			// sort the invoices by date serviced prior to charging them to stripe - the invoice will look a lot
			// cleaner this way
			date_sorted_invoices := make(invoiceByTime, 0, len(invoices))
			for _, d := range invoices {
				date_sorted_invoices = append(date_sorted_invoices, d)
			}

			sort.Sort(date_sorted_invoices)

			updateFields := map[string]interface{}{}

			// set paid and notes column for patch update
			confirmationNumber, err := chargeStripe(customerID, invoices)
			if err != nil {
				updateFields[notesColumn] = fmt.Sprintf("Error charging customer through Stripe: %v", err.Error())
				updateFields[paidColumn] = "false"
			} else {
				updateFields[notesColumn] = confirmationNumber
				updateFields[paidColumn] = "true"
			}

			for _, invoice := range invoices {
				// update only the notes and paid columns
				updatedRecord := airtable.Record{ID: invoice.airtableRecordID, Fields: updateFields}
				err = airtableClient.PartialUpdate(airtable.PartialUpdateOptions{TableName: tableName}, updatedRecord)
				if err != nil {
					log.Printf("error updating airtable records %v", err)
					continue
				}
			}

			// don't overload the Airtable API
			time.Sleep(time.Second * 1)
		}

		time.Sleep(60 * time.Second)
		fmt.Println("Starting new processing loop...")
	}

}

// fetches the airtable records, accepts an offset parameter if the previous response isn't the end of the records
func fetchAirtableRecords(offset string) (airtable.ListResponse, error) {
	options := airtable.ListRecordsOptions{
		TableName: os.Getenv("TABLENAME"),
		Fields: []string{
			os.Getenv("STRIPE_CUSTOMER_ID_COLUMN"),
			os.Getenv("INVOICE_AMOUNT_COLUMN"),
			os.Getenv("PAID_COLUMN"),
			os.Getenv("CURRENCY_CODE_COLUMN"),
			os.Getenv("DATE_COLUMN"),
			os.Getenv("QUANTITY_COLUMN"),
			os.Getenv("ITEM_COLUMN"),
			os.Getenv("PROPERTY_COLUMN"),
			os.Getenv("DATE_SERVICED_COLUMN"),
		},
		FilterByFormula: fmt.Sprintf(`AND(NOT({%s} = 'true'), NOT({%s} = 'false'))`, os.Getenv("PAID_COLUMN"), os.Getenv("PAID_COLUMN")),
		PageSize:        100, // max records return allowed from airtable
	}

	if offset != "" {
		options.Offset = offset
	}

	airtableClient, err := airtable.NewAirtableClient(os.Getenv("AIRTABLE_API_KEY"), os.Getenv("AIRTABLE_BASE_ID"))
	if err != nil {
		return airtable.ListResponse{}, err
	}

	return airtableClient.ListFromTable(options)
}

// given the correct information pull a customer's payment methods and charge the provided amount to Stripe
func chargeStripe(customerID string, items []InvoiceItem) (invoiceID string, err error) {
	if len(items) <= 0 {
		return "", nil
	}

	stripe.Key = os.Getenv("STRIPE_API_KEY")

	// add each item to the invoice
	for _, item := range items {
		var amount int64

		switch item.currencyCode {
		case "usd":
			amount = int64(item.amount * 100)
		default:
			continue
		}

		if amount <= 0 {
			continue
		}

		// TODO: if you want this to actually respect the quantity then the airtable invoice table needs to be modified
		_, err = invoiceitem.New(&stripe.InvoiceItemParams{
			Customer:    stripe.String(customerID),
			Amount:      stripe.Int64(amount),
			Currency:    stripe.String(item.currencyCode),
			Description: stripe.String(fmt.Sprintf("%s for %s on %s", item.item, item.property, item.dateServiced.String())),
		})

		if err != nil {
			log.Printf("unable to create stripe invoice item for airtable record %s - %v", item.airtableRecordID, err.Error())
		}
	}

	in, err := invoice.New(&stripe.InvoiceParams{
		Customer:         stripe.String(customerID),
		AutoAdvance:      stripe.Bool(true),
		CollectionMethod: stripe.String("send_invoice"),
		DaysUntilDue:     stripe.Int64(30),
		Description:      stripe.String("Weekly cleaning and item replacement charges for properties managed."),
	})

	return in.ID, err
}
