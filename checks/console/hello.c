/*
 * The emulator console: a program that writes a line one byte at a time to
 * register 0xfff0 (address 0x80fff0), which run_boot.py --console and
 * run-boot --console collect, then calls _exit(), which (as the minimal
 * libc's does) prints "exit" and spins; --stop-at _exit ends the run at its entry,
 * before that line.
 * Built by checks/console/build.sh with the sem start code.
 * SPDX-License-Identifier: Apache-2.0
 */
#include <stdint.h>

#define CONSOLE (*(volatile uint8_t *)0x0080fff0u)

volatile uint32_t done;

__attribute__((noinline)) void _exit(int code)
{
	const char *s = "exit\n";

	(void)code;
	while (*s) {
		CONSOLE = (uint8_t)*s++;
	}
	for (;;) {
	}
}

void sem_main(void)
{
	const char *s = "hello from tc32\n<dbg> zmk: hid_listener_keycode_pressed: usage_page 0x07 keycode 0x04\n";

	while (*s) {
		CONSOLE = (uint8_t)*s++;
	}
	done = 0x600dc0deu;
	_exit(0);
}
