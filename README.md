This program processed all .csv sales report located in `sales/unprocessed`, stores the csv data into a postgres instance, and moves the report to `sales/processed`. The program is idempotent, i.e. - processing the same report multiple times will not affect the database; however, if the data within an old report is changed, the database will be updated to mirror the new changes.

Currently, the program was made to process sales reports from the Toast system. See example file
sales/unprocessed/20220122.csv
``` csv
Order Id,Order #,Sent Date,Item Id,Menu Item,Menu Subgroup(s),Menu Group,Menu,Sales Category,Gross Price,Discount,Net Price,Qty,Tax,Void?
5740022836216005,1,1/21/23 1:17 PM,5740019922563741,Wings,,Chicken,YumYums All Day Menu,Food,14.75,0.00,14.75,1.0,0.88000000,false
5740022836789251,2,1/21/23 1:29 PM,5740022830259583,Jerked Chicken Sliders,,Chicken,YumYums All Day Menu,Food,12.50,0.00,12.50,1.0,0.75000000,false
```

# SFTP
Toast allows pulling reports via sftp
``` bash
sftp -i ~/.ssh/id_rsa -r YumYumsExportUser@s-9b0f88558b264dfda.server.transfer.us-east-1.amazonaws.com:/113866
```