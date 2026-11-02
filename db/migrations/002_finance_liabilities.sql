-- Finance v0.2: additive liability, debt, and installment support.
BEGIN;

ALTER TABLE mimo.finance_accounts
  ADD COLUMN IF NOT EXISTS classification text NOT NULL DEFAULT 'asset',
  ADD COLUMN IF NOT EXISTS credit_limit_idr bigint;

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='finance_accounts_classification_check') THEN
    ALTER TABLE mimo.finance_accounts ADD CONSTRAINT finance_accounts_classification_check CHECK (classification IN ('asset','liability'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='finance_accounts_credit_limit_check') THEN
    ALTER TABLE mimo.finance_accounts ADD CONSTRAINT finance_accounts_credit_limit_check CHECK (credit_limit_idr IS NULL OR credit_limit_idr >= 0);
  END IF;
END $$;

ALTER TABLE mimo.finance_accounts DROP CONSTRAINT IF EXISTS finance_accounts_kind_check;
ALTER TABLE mimo.finance_accounts
  ADD CONSTRAINT finance_accounts_kind_check CHECK (kind IN ('cash','bank','ewallet','paylater','credit_card','loan','other'));

ALTER TABLE mimo.finance_transactions DROP CONSTRAINT IF EXISTS finance_transactions_type_check;
ALTER TABLE mimo.finance_transactions
  ADD CONSTRAINT finance_transactions_type_check CHECK (type IN ('income','expense','transfer','liability_purchase','debt_repayment','liability_fee'));
ALTER TABLE mimo.finance_transactions DROP CONSTRAINT IF EXISTS finance_transactions_check;
ALTER TABLE mimo.finance_transactions
  ADD CONSTRAINT finance_transactions_shape_check CHECK (
    (type='expense' AND from_account_id IS NOT NULL AND to_account_id IS NULL)
    OR (type='income' AND to_account_id IS NOT NULL AND from_account_id IS NULL)
    OR (type='transfer' AND from_account_id IS NOT NULL AND to_account_id IS NOT NULL AND from_account_id<>to_account_id AND category_id IS NULL)
    OR (type IN ('liability_purchase','liability_fee') AND from_account_id IS NULL AND to_account_id IS NOT NULL)
    OR (type='debt_repayment' AND from_account_id IS NOT NULL AND to_account_id IS NOT NULL AND from_account_id<>to_account_id AND category_id IS NULL)
  );
ALTER TABLE mimo.finance_transactions
  ADD COLUMN IF NOT EXISTS debt_id uuid,
  ADD COLUMN IF NOT EXISTS parent_transaction_id uuid REFERENCES mimo.finance_transactions(id);

CREATE TABLE IF NOT EXISTS mimo.finance_debts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL,
  liability_account_id uuid NOT NULL REFERENCES mimo.finance_accounts(id),
  purchase_transaction_id uuid NOT NULL UNIQUE REFERENCES mimo.finance_transactions(id),
  principal_idr bigint NOT NULL CHECK (principal_idr > 0),
  total_fee_idr bigint NOT NULL DEFAULT 0 CHECK (total_fee_idr >= 0),
  tenor smallint NOT NULL CHECK (tenor > 0 AND tenor <= 120),
  first_due_date date NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','paid','voided')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS finance_debts_owner_account_idx ON mimo.finance_debts(owner_id,liability_account_id,status);

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='finance_transactions_debt_fk') THEN
    ALTER TABLE mimo.finance_transactions ADD CONSTRAINT finance_transactions_debt_fk FOREIGN KEY (debt_id) REFERENCES mimo.finance_debts(id);
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS mimo.finance_installments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  debt_id uuid NOT NULL REFERENCES mimo.finance_debts(id) ON DELETE CASCADE,
  installment_number smallint NOT NULL CHECK (installment_number > 0),
  due_date date NOT NULL,
  principal_due_idr bigint NOT NULL CHECK (principal_due_idr > 0),
  fee_due_idr bigint NOT NULL DEFAULT 0 CHECK (fee_due_idr >= 0),
  principal_paid_idr bigint NOT NULL DEFAULT 0 CHECK (principal_paid_idr >= 0 AND principal_paid_idr <= principal_due_idr),
  fee_paid_idr bigint NOT NULL DEFAULT 0 CHECK (fee_paid_idr >= 0 AND fee_paid_idr <= fee_due_idr),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','partial','paid','overdue')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(debt_id,installment_number)
);
CREATE INDEX IF NOT EXISTS finance_installments_due_idx ON mimo.finance_installments(due_date,status);
CREATE INDEX IF NOT EXISTS finance_installments_debt_idx ON mimo.finance_installments(debt_id,installment_number);

CREATE TABLE IF NOT EXISTS mimo.finance_installment_payments (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  installment_id uuid NOT NULL REFERENCES mimo.finance_installments(id) ON DELETE RESTRICT,
  transaction_id uuid NOT NULL REFERENCES mimo.finance_transactions(id) ON DELETE RESTRICT,
  principal_paid_idr bigint NOT NULL DEFAULT 0 CHECK (principal_paid_idr >= 0),
  fee_paid_idr bigint NOT NULL DEFAULT 0 CHECK (fee_paid_idr >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (principal_paid_idr + fee_paid_idr > 0)
);
CREATE INDEX IF NOT EXISTS finance_installment_payments_installment_idx ON mimo.finance_installment_payments(installment_id);
CREATE INDEX IF NOT EXISTS finance_installment_payments_transaction_idx ON mimo.finance_installment_payments(transaction_id);

CREATE INDEX IF NOT EXISTS finance_transactions_debt_idx ON mimo.finance_transactions(debt_id,status,occurred_at DESC);

DROP TRIGGER IF EXISTS finance_debts_touch ON mimo.finance_debts;
CREATE TRIGGER finance_debts_touch BEFORE UPDATE ON mimo.finance_debts FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS finance_installments_touch ON mimo.finance_installments;
CREATE TRIGGER finance_installments_touch BEFORE UPDATE ON mimo.finance_installments FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();

COMMIT;
