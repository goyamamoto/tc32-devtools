/*
 * run-boot --stacks paints the thread stacks only: a Zephyr image keeps them
 * in the noinit section, and other objects whose names contain "stack" keep
 * their contents. thread_stack is in noinit; obj_type_stack, in .data, stands
 * for a kernel object type such as Zephyr's. z_cstart() (where --stacks
 * paints) uses the top 32 bytes of thread_stack; then the program writes
 * obj_type_stack's first word in hex to the console (register 0xfff0) and
 * calls _exit(), where --stop-at _exit ends the run.
 * Built by checks/console/build.sh with the sem start code and stacks.ld.
 * SPDX-License-Identifier: Apache-2.0
 */
#include <stdint.h>

#define CONSOLE (*(volatile uint8_t *)0x0080fff0u)

__attribute__((section("noinit"))) volatile uint8_t thread_stack[256];
volatile uint32_t obj_type_stack[17] = {0x4b435453u};

__attribute__((noinline)) void z_cstart(void)
{
	for (int i = 0; i < 32; i++) {
		thread_stack[255 - i] = 0;
	}
}

__attribute__((noinline)) void _exit(int code)
{
	(void)code;
	for (;;) {
	}
}

void sem_main(void)
{
	static const char hex[] = "0123456789abcdef";
	uint32_t v;

	z_cstart();
	v = obj_type_stack[0];
	for (int s = 28; s >= 0; s -= 4) {
		CONSOLE = (uint8_t)hex[(v >> s) & 15u];
	}
	CONSOLE = '\n';
	_exit(0);
}
