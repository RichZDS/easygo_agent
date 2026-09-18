---
name: strict-arithmetic
description: Use when the user requests arithmetic or an exact numeric product, sum, difference, or quotient.
---
# Strict Arithmetic

Call `calculator` for each requested arithmetic operation. Use the returned value without rounding unless requested.

Acceptance: the final answer starts with `CALC:` followed immediately by the result, such as `CALC:578`. A tool failure is not a numeric result; load `tool-error-first` if calculation fails. For combined procedures, apply their formatting to the remaining answer after the calculation prefix unless the user specifies another format.
