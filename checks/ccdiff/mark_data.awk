# Mark data inside gcc's assembly output for forms_check.py: a label
# __cd_d<N> where data directives (literal pools, switch tables, strings)
# start and __cd_t<N> where instructions start again. The labels are local
# symbols that change no layout; Telink's assembler writes no mapping symbols
# of its own.
# SPDX-License-Identifier: Apache-2.0
BEGIN { data = 0; n = 0 }
/^\t\.(byte|2byte|4byte|short|hword|word|long|ascii|asciz|string|space|zero|fill)([ \t]|$)/ {
	if (!data) { printf "__cd_d%d:\n", n++; data = 1 }
	print
	next
}
/^\t[a-z]/ {
	if (data) { printf "__cd_t%d:\n", n++; data = 0 }
}
{ print }
