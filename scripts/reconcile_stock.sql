-- reconcile_stock.sql
-- One-time reconciliation for stock cost corruption:
--
--   1. Soft-deletes stock ledger entries whose source document was deleted
--      (e.g. invoices deleted while "keep stock transaction history" was in
--      effect). Restores had already put qty back into inventory_stocks, so
--      the surviving entries double-subtract in snapshot replays and inflate
--      the unexplained baseline.
--
--   2. Recomputes inventory_stocks.average_cost from the weighted average of
--      costed inflows (purchase/opening/adjustment, qty>0, cost>0, approved,
--      not deleted) matching each stock row's product+outlet+batch. Fallback
--      chain: batch-level WAC -> product+outlet WAC -> product-wide WAC ->
--      products.purchase_price -> keep existing. This removes corruption from
--      restores that re-rated at selling price and from WAC division by
--      negative/near-zero quantities.
--
-- HOW TO RUN
--   Dry run (report only, nothing persisted):
--       psql -d truerp -v dry_run=1 -f scripts/reconcile_stock.sql
--   Apply:
--       psql -d truerp -f scripts/reconcile_stock.sql
--
-- After applying, stock snapshots rebuild automatically on the next report
-- read (entry deletions + stock updated_at changes alter the fingerprint).

\set ON_ERROR_STOP on

BEGIN;

-- ============================================================================
-- PHASE 1 — orphan stock ledger entries (source document deleted)
-- ============================================================================

\echo '=== PHASE 1: orphaned stock_entries to remove ==='
SELECT se.reference_type, se.entry_type, COUNT(*) AS entries,
       SUM(se.quantity) AS total_qty
FROM stock_entries se
WHERE se.deleted_at IS NULL AND se.reference_id IS NOT NULL AND (
    (se.reference_type = 'invoice' AND EXISTS (
        SELECT 1 FROM invoices i WHERE i.id = se.reference_id AND i.deleted_at IS NOT NULL))
 OR (se.reference_type = 'purchase_bill' AND EXISTS (
        SELECT 1 FROM purchase_bills b WHERE b.id = se.reference_id AND b.deleted_at IS NOT NULL))
 OR (se.reference_type = 'sales_return' AND EXISTS (
        SELECT 1 FROM sales_returns r WHERE r.id = se.reference_id AND r.deleted_at IS NOT NULL))
 OR (se.reference_type = 'purchase_return' AND EXISTS (
        SELECT 1 FROM purchase_returns r WHERE r.id = se.reference_id AND r.deleted_at IS NOT NULL))
)
GROUP BY se.reference_type, se.entry_type
ORDER BY 1, 2;

UPDATE stock_entries se SET deleted_at = NOW()
WHERE se.deleted_at IS NULL AND se.reference_id IS NOT NULL AND (
    (se.reference_type = 'invoice' AND EXISTS (
        SELECT 1 FROM invoices i WHERE i.id = se.reference_id AND i.deleted_at IS NOT NULL))
 OR (se.reference_type = 'purchase_bill' AND EXISTS (
        SELECT 1 FROM purchase_bills b WHERE b.id = se.reference_id AND b.deleted_at IS NOT NULL))
 OR (se.reference_type = 'sales_return' AND EXISTS (
        SELECT 1 FROM sales_returns r WHERE r.id = se.reference_id AND r.deleted_at IS NOT NULL))
 OR (se.reference_type = 'purchase_return' AND EXISTS (
        SELECT 1 FROM purchase_returns r WHERE r.id = se.reference_id AND r.deleted_at IS NOT NULL))
);

-- ============================================================================
-- PHASE 2 — recompute inventory_stocks.average_cost
-- ============================================================================

\echo '=== PHASE 2: average_cost corrections (top 40 by abs delta) ==='
WITH costed_batch AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           COALESCE(TRIM(se.batch_no), '') AS bno,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3, 4
),
costed_product AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3
),
costed_anywhere AS (
    SELECT se.user_id, se.product_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2
),
proposed AS (
    SELECT s.id AS stock_id, p.name AS product_name, s.batch_no, s.quantity,
           s.average_cost AS old_cost,
           COALESCE(cb.wac, cp.wac, ca.wac,
                    NULLIF(p.purchase_price, 0), s.average_cost) AS new_cost
    FROM inventory_stocks s
    JOIN products p ON p.id = s.product_id AND p.user_id = s.user_id
    LEFT JOIN costed_batch cb
        ON cb.user_id = s.user_id AND cb.product_id = s.product_id
       AND cb.outlet_id = s.outlet_id AND cb.bno = COALESCE(TRIM(s.batch_no), '')
    LEFT JOIN costed_product cp
        ON cp.user_id = s.user_id AND cp.product_id = s.product_id
       AND cp.outlet_id = s.outlet_id
    LEFT JOIN costed_anywhere ca
        ON ca.user_id = s.user_id AND ca.product_id = s.product_id
)
SELECT product_name, batch_no, quantity, old_cost, new_cost,
       (new_cost - old_cost) AS delta
FROM proposed
WHERE ABS(new_cost - old_cost) > 0.01
ORDER BY ABS(new_cost - old_cost) DESC
LIMIT 40;

\echo '=== PHASE 2: summary of rows changed ==='
WITH costed_batch AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           COALESCE(TRIM(se.batch_no), '') AS bno,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3, 4
),
costed_product AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3
),
costed_anywhere AS (
    SELECT se.user_id, se.product_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2
),
proposed AS (
    SELECT s.id AS stock_id, p.name AS product_name, s.batch_no, s.quantity,
           s.average_cost AS old_cost,
           COALESCE(cb.wac, cp.wac, ca.wac,
                    NULLIF(p.purchase_price, 0), s.average_cost) AS new_cost
    FROM inventory_stocks s
    JOIN products p ON p.id = s.product_id AND p.user_id = s.user_id
    LEFT JOIN costed_batch cb
        ON cb.user_id = s.user_id AND cb.product_id = s.product_id
       AND cb.outlet_id = s.outlet_id AND cb.bno = COALESCE(TRIM(s.batch_no), '')
    LEFT JOIN costed_product cp
        ON cp.user_id = s.user_id AND cp.product_id = s.product_id
       AND cp.outlet_id = s.outlet_id
    LEFT JOIN costed_anywhere ca
        ON ca.user_id = s.user_id AND ca.product_id = s.product_id
)
SELECT COUNT(*) AS rows_changed,
       SUM(quantity * (new_cost - old_cost)) AS total_value_delta
FROM proposed
WHERE ABS(new_cost - old_cost) > 0.01;

-- Apply the recompute.
WITH costed_batch AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           COALESCE(TRIM(se.batch_no), '') AS bno,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3, 4
),
costed_product AS (
    SELECT se.user_id, se.product_id, se.outlet_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2, 3
),
costed_anywhere AS (
    SELECT se.user_id, se.product_id,
           SUM(se.quantity * se.cost_price) / NULLIF(SUM(se.quantity), 0) AS wac
    FROM stock_entries se
    WHERE se.deleted_at IS NULL
      AND (se.approval_status = 'approved' OR se.approval_status = '' OR se.approval_status IS NULL)
      AND se.entry_type IN ('purchase', 'opening', 'adjustment')
      AND se.quantity > 0 AND se.cost_price > 0
      AND se.product_id IS NOT NULL
    GROUP BY 1, 2
),
proposed AS (
    SELECT s.id AS stock_id,
           s.average_cost AS old_cost,
           COALESCE(cb.wac, cp.wac, ca.wac,
                    NULLIF(p.purchase_price, 0), s.average_cost) AS new_cost
    FROM inventory_stocks s
    JOIN products p ON p.id = s.product_id AND p.user_id = s.user_id
    LEFT JOIN costed_batch cb
        ON cb.user_id = s.user_id AND cb.product_id = s.product_id
       AND cb.outlet_id = s.outlet_id AND cb.bno = COALESCE(TRIM(s.batch_no), '')
    LEFT JOIN costed_product cp
        ON cp.user_id = s.user_id AND cp.product_id = s.product_id
       AND cp.outlet_id = s.outlet_id
    LEFT JOIN costed_anywhere ca
        ON ca.user_id = s.user_id AND ca.product_id = s.product_id
)
UPDATE inventory_stocks s
SET average_cost = proposed.new_cost,
    updated_at   = NOW()
FROM proposed
WHERE s.id = proposed.stock_id
  AND ABS(proposed.new_cost - proposed.old_cost) > 0.01;

-- ============================================================================
-- PHASE 3 — residual anomalies (report only, manual review needed)
-- ============================================================================

\echo '=== PHASE 3: negative-quantity stock rows (manual review) ==='
SELECT p.name, s.batch_no, s.quantity, s.average_cost
FROM inventory_stocks s JOIN products p ON p.id = s.product_id
WHERE s.quantity < 0
ORDER BY s.quantity * s.average_cost;

\if :{?dry_run}
  \echo '=== DRY RUN — rolling back, nothing was persisted ==='
  ROLLBACK;
\else
  COMMIT;
\endif
