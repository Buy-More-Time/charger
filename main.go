package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stripe/stripe-go/v71"
	"github.com/stripe/stripe-go/v71/client"

	"github.com/dnoberon/charger/airtable"

	"github.com/joho/godotenv"
)

type Invoice struct {
	airtableRecordID string
	invoiceAmount    float64
	currencyCode     string
}

func main() {
	var pollInterval int
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

	staleDays, err := strconv.Atoi(os.Getenv("STALE_DAYS"))
	if err != nil {
		staleDays = -7
	}

	pollInterval, err = strconv.Atoi(os.Getenv("POLL_INTERVAL"))
	if err != nil {
		pollInterval = 60
	}

	airtableClient, err := airtable.NewAirtableClient(airtableAPIKey, baseID)
	if err != nil {
		log.Fatal(err)
	}

	// start process loop
	go func() {
		for {
			// fetch all unpaid invoice records
			records, err := airtableClient.ListFromTable(airtable.ListRecordsOptions{
				TableName: tableName,
				Fields: []string{
					stripeCustomerIDColumn,
					invoiceAmountColumn,
					paidColumn,
					currencyCodeColumn,
					dateColumn,
				},
				// avoid fetching already paid, or failed invoices
				FilterByFormula: fmt.Sprintf(`AND(NOT({%s} = 'true'), NOT({%s} = 'false'))`, paidColumn, paidColumn),
				PageSize:        100, // max records return allowed from airtable TODO: Handle pagination when necessary, right now the numbers are too low
			})

			if err != nil {
				log.Printf("error fetching airtable records %v", err)
				return
			}

			// generate a list of all invoices for a given customer, BuyMoreTime wants to chargeStripe all the customer's
			// invoices at once instead of each individually
			customerInvoices := map[string][]Invoice{}
			for _, record := range records.Records {
				// verify that if we have a value in the paid column, it's not true or false
				val, ok := record.Fields[paidColumn]
				if ok {
					if fmt.Sprintf("%v", val) == "true" {
						log.Printf("customer already charged for this record, skipping")
						continue
					}
				}

				invoice := Invoice{}
				// check to see if theres a date, bill only on or after said date
				loc, err := time.LoadLocation(os.Getenv("TIMEZONE"))
				if err != nil {
					log.Fatal("incorrect time location!")
					return
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

						past := time.Now().AddDate(0, 0, staleDays)

						if date.Before(past) {
							log.Printf("pay date too old, skipping")
							continue
						}
					}

				} else if !ok {
					log.Printf("date not present, skipping")
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

				currencyCode := fmt.Sprintf("%s", val)
				currencyCode = strings.ToLower(currencyCode)

				// TODO: If you want to handle more types of currency you will need to refactor this and the function below to make it work
				if currencyCode != "usd" {
					log.Printf("unsupported currency code, skipping")
					continue
				}

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

				invoice.currencyCode = currencyCode
				invoice.invoiceAmount = invoiceAmount.(float64)
				invoice.airtableRecordID = record.ID

				customerInvoices[customerID] = append(customerInvoices[customerID], invoice)
			}

			// for each customer create a chargeStripe for all invoices sharing the same currency
			for customerID, invoices := range customerInvoices {
				var amount float64
				var currencyCode string

				// sum the invoices
				for _, invoice := range invoices {
					amount += invoice.invoiceAmount

					// TODO: if you plan on support multiple currencies, this will need to be ripped out and redone
					currencyCode = invoice.currencyCode
				}

				updateFields := map[string]interface{}{}

				// set paid and notes column for patch update
				confirmationNumber, err := chargeStripe(customerID, currencyCode, amount)
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
						return
					}
				}

				// don't overload the Airtable API
				time.Sleep(time.Second * 1)
			}

			time.Sleep(time.Duration(pollInterval) * time.Second)
		}
	}()

	fmt.Println("Charger Running....")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc
}

// given the correct information pull a customer's payment methods and charge the provided amount to Stripe
func chargeStripe(customerID string, currencyCode string, invoiceAmount float64) (confirmation string, err error) {
	var amount int64

	sc := &client.API{}
	sc.Init(os.Getenv("STRIPE_API_KEY"), nil)

	switch currencyCode {
	case "usd":
		amount = int64(invoiceAmount * 100)
	default:
		return "", errors.New("currency not supported")
	}

	if amount <= 0 {
		return "", errors.New("cannot chargeStripe 0 amount")
	}

	i := sc.PaymentMethods.List(&stripe.PaymentMethodListParams{
		Customer: stripe.String(fmt.Sprintf("%v", customerID)),
		Type:     stripe.String("card"),
	})

	if i.Err() != nil {
		return "", i.Err()
	}

	var paid bool

	for i.Next() && !paid {
		paymentID := i.PaymentMethod().ID

		pi, err := sc.PaymentIntents.New(&stripe.PaymentIntentParams{
			Amount:        stripe.Int64(amount),
			Description:   stripe.String("Cleaning/Product Replacement Charge"),
			Customer:      stripe.String(fmt.Sprintf("%v", customerID)),
			Currency:      stripe.String(fmt.Sprintf("%s", currencyCode)),
			PaymentMethod: stripe.String(paymentID),
		})

		if err != nil {
			return "", err
		}

		confirm, err := sc.PaymentIntents.Confirm(pi.ID, &stripe.PaymentIntentConfirmParams{
			PaymentMethod: stripe.String(paymentID),
		})

		if err != nil {
			return "", err
		}

		return confirm.ID, nil
	}

	return "", errors.New("unable to chargeStripe any payment method on file")
}
