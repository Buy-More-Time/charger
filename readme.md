# Charger
Charger is an **experimental** command line application capable of polling an Airtable table of invoices and charging Stripe for each provided record. This application was built so that we could auto-charge customers who provided their card information to us without having to leave Airtable or charge them manually via Stripe.

### How To Use
#### Application Setup
Here are some required environment variables needed for this application, the rest are in the next section.

| | |
| ------------- | ------------- |
| STRIPE_API_KEY | Stripe developer API key |
| AIRTABLE_API_KEY |  Airtable developer API key |
| AIRTABLE_BASE_ID |  Airtable Base ID |
| TABLE_NAME | Airtable invoice table name |
| POLL_INTERVAL | Time in seconds between each poll of Airtable invoice records|

#### Table Setup
Charger expects your table to have at least five separate columns. These columns should be created beforehand and are provided to the application through environment variables. These are the environment variables.

| Environment Variable | Column Type|
| ------------- | ------------- |
| STRIPE_CUSTOMER_ID_COLUMN | `string` - Stripe Customer ID for the invoiced person or organization |
| INVOICE_AMOUNT_COLUMN | `float` - Invoice Amount | 
| CURRENCY_CODE_COLUMN | `string` - Three Digit Currency Code (currently only `usd` is accepted)| 
| PAID_COLUMN | `string` - Either "true" or anything else. Indicates whether or not a record was paid |
| NOTES_COLUMN | `string` - Will record a payment reference number on success, or error information on issues |
| DATE_COLUMN | `string` -  Date on which the invoice should be charged |


#### Run
Simply run the application with the above environment variables present and Charger will continue to monitor and charge invoices until exited or terminated.

#### Deployment
I've added a Dockerfile and a Github Workflow for using Github as a secrets repository and deploying the application on AWS's ECS platform. Good Luck!
