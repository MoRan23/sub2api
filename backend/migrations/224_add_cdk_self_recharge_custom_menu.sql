-- Add the default CDK self-recharge storefront to the existing custom menu.
-- Existing menu entries and an administrator-managed entry with the same ID
-- are left untouched.

DO $$
DECLARE
    v_raw        text;
    v_items      jsonb;
    v_new_item   jsonb;
    v_icon       text;
    v_sort_order integer;
BEGIN
    SELECT value INTO v_raw
      FROM settings
     WHERE key = 'custom_menu_items';

    IF COALESCE(TRIM(v_raw), '') = '' OR TRIM(v_raw) = 'null' THEN
        v_items := '[]'::jsonb;
    ELSE
        BEGIN
            v_items := v_raw::jsonb;
        EXCEPTION WHEN others THEN
            RAISE WARNING '[migration-224] custom_menu_items is not valid JSON; preserving the existing value';
            RETURN;
        END;
    END IF;

    IF jsonb_typeof(v_items) IS DISTINCT FROM 'array' THEN
        RAISE WARNING '[migration-224] custom_menu_items is not an array; preserving the existing value';
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
          FROM jsonb_array_elements(v_items) AS elem
         WHERE elem ->> 'id' = 'cdk_self_recharge'
    ) THEN
        RETURN;
    END IF;

    SELECT COALESCE(
        MAX(
            CASE
                WHEN elem ->> 'sort_order' ~ '^-?[0-9]+$'
                    THEN (elem ->> 'sort_order')::integer
                ELSE NULL
            END
        ),
        -1
    ) + 1
      INTO v_sort_order
      FROM jsonb_array_elements(v_items) AS elem;

    v_icon := '<svg fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="M2.25 8.25h19.5M2.25 9h19.5m-16.5 5.25h6m-6 2.25h3m-3.75 3h15a2.25 2.25 0 002.25-2.25V6.75A2.25 2.25 0 0019.5 4.5h-15a2.25 2.25 0 00-2.25 2.25v10.5A2.25 2.25 0 004.5 19.5z"/></svg>';

    v_new_item := jsonb_build_object(
        'id',            'cdk_self_recharge',
        'label',         'CDK 自助充值',
        'icon_svg',      v_icon,
        'url',           'https://pay.ldxp.cn/shop/XR56YHVH',
        'visibility',    'user',
        'purchase_mode', true,
        'sort_order',    v_sort_order
    );

    v_items := v_items || jsonb_build_array(v_new_item);

    INSERT INTO settings (key, value)
    VALUES ('custom_menu_items', v_items::text)
    ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
END $$;
